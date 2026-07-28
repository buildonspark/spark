package ent_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entephemeral"
	ephemeralenttest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func TestSigningKeyshareGetSecretShare_MainSecretPreferredWithoutEphemeral(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	secret := keys.MustParsePrivateKeyHex("adeab186b64a2239f15640cb43d7c57c35376f5e1c42f574671880a34a4a80ad")

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &secret, nil)
	resolved, err := keyshare.GetSecretShare(ctx)
	require.NoError(t, err)
	require.Equal(t, secret, *resolved)
}

func TestSigningKeyshareGetSecretShare_ErrWhenEphemeralUnavailable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, new(int32(0)))
	_, err := keyshare.GetSecretShare(ctx)
	require.Error(t, err)
	require.ErrorContains(t, err, "ephemeral DB is unavailable")
	require.ErrorIs(t, err, ent.ErrSigningKeyshareSecretUnavailable)
}

func TestSigningKeyshareGetSecretShare_LoadsFromEphemeralWhenMainSecretNil(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:ephemeral_get_secret_share_ok?mode=memory&_fk=1")
	t.Cleanup(func() {
		_ = ephemeralClient.Close()
	})

	version := int32(0)
	secret := keys.MustParsePrivateKeyHex("5ab9bcbbf7e7073f5d6fd5cb56af8f3d4f77d8a7c356c9f67018a2ac8d15f11a")
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)

	_, err := ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetVersion(version).
		SetSecretShare(secret).
		Save(ctx)
	require.NoError(t, err)

	ctxWithEphemeral := entephemeral.Inject(ctx, db.NewReadOnlyEphemeralSession(ctx, ephemeralClient))
	resolved, err := keyshare.GetSecretShare(ctxWithEphemeral)
	require.NoError(t, err)
	require.Equal(t, secret, *resolved)
}

func TestSigningKeyshareGetSecretShare_ErrWhenEphemeralVersionMissing(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:ephemeral_get_secret_share_missing?mode=memory&_fk=1")
	t.Cleanup(func() {
		_ = ephemeralClient.Close()
	})

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, new(int32(7)))

	ctxWithEphemeral := entephemeral.Inject(ctx, db.NewReadOnlyEphemeralSession(ctx, ephemeralClient))
	_, err := keyshare.GetSecretShare(ctxWithEphemeral)
	require.Error(t, err)
	require.ErrorContains(t, err, "was not found in ephemeral DB")
	require.ErrorIs(t, err, ent.ErrSigningKeyshareSecretMissing)
}

func TestPrepareSigningKeyshareCreateWithSecret_FallsBackToMainDBWhenEphemeralUnavailable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	keyID := uuid.New()
	secret := keys.MustParsePrivateKeyHex("53ff19722a261a55b7f67dfc6f95b5a4f95f4af6d66bdff03422ad10240cb9ed")
	publicKeySource := keys.MustParsePrivateKeyHex("31f98c9db585d9138b9083ec0d0a86a8ce4f383e1281870e7d56f2ea54f183de")

	create := tc.Client.SigningKeyshare.Create().
		SetID(keyID).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{"1": publicKeySource.Public()}).
		SetPublicKey(publicKeySource.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)

	create, err := ent.PrepareSigningKeyshareCreateWithSecret(ctx, create, keyID, secret)
	require.NoError(t, err)

	created, err := create.Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, created.SecretShare)
	require.Equal(t, secret, *created.SecretShare)
	require.Nil(t, created.SecretVersion)
}

func TestUpdateSigningKeyshareWithRotatedSecret_FallsBackToMainDBWhenEphemeralUnavailable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	newSecret := keys.MustParsePrivateKeyHex("ee5f45be26ef9a5fe3e29ea9d2cb4f1200519676ad958962f4f7dcae998f1a16")
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, new(keys.MustParsePrivateKeyHex("fd9627ee6b0fd2f6a14833ea637f5f3af8d7e4f2a5ee5ec92fae13496f95da60")), new(int32(7)))
	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(
		ctx,
		keyshare.ID,
		keyshare.SecretVersion,
		newSecret,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretShare)
	require.Equal(t, newSecret, *updated.SecretShare)
	require.Nil(t, updated.SecretVersion)
}

func TestPrepareSigningKeyshareCreateWithSecret_UsesEphemeralAndDualWritesWhenEnabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	keyID := uuid.New()
	secret := keys.MustParsePrivateKeyHex("7cfb5322f5ba892194f59fd868ab89c7ea3d5f9531d3460f79dd0f46efefcd8f")
	publicKeySource := keys.MustParsePrivateKeyHex("bc605b157cf626f43108cce5fcd6ea7feb7138319d427f6015f4cb8918ea4a22")

	create := tc.Client.SigningKeyshare.Create().
		SetID(keyID).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{"1": publicKeySource.Public()}).
		SetPublicKey(publicKeySource.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)

	create, err := ent.PrepareSigningKeyshareCreateWithSecret(ctx, create, keyID, secret)
	require.NoError(t, err)

	created, err := create.Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, created.SecretVersion)
	require.Equal(t, int32(0), *created.SecretVersion)
	require.NotNil(t, created.SecretShare)
	require.Equal(t, secret, *created.SecretShare)

	ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyID, *created.SecretVersion)
	require.NoError(t, err)
	require.True(t, ephemeralSecret.SecretShare.Equals(secret))
}

func TestPrepareSigningKeyshareCreateWithSecret_UsesEphemeralWithoutDualWriteWhenDisabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	keyID := uuid.New()
	secret := keys.MustParsePrivateKeyHex("7cfb5322f5ba892194f59fd868ab89c7ea3d5f9531d3460f79dd0f46efefcd8f")
	publicKeySource := keys.MustParsePrivateKeyHex("bc605b157cf626f43108cce5fcd6ea7feb7138319d427f6015f4cb8918ea4a22")

	create := tc.Client.SigningKeyshare.Create().
		SetID(keyID).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{"1": publicKeySource.Public()}).
		SetPublicKey(publicKeySource.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)

	create, err := ent.PrepareSigningKeyshareCreateWithSecret(ctx, create, keyID, secret)
	require.NoError(t, err)

	created, err := create.Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, created.SecretVersion)
	require.Equal(t, int32(0), *created.SecretVersion)
	require.Nil(t, created.SecretShare)

	ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyID, *created.SecretVersion)
	require.NoError(t, err)
	require.True(t, ephemeralSecret.SecretShare.Equals(secret))

	require.NoError(t, ent.HydrateSigningKeyshareSecrets(ctx, []*ent.SigningKeyshare{created}))
	resolvedSecret, err := created.GetSecretShare(ctx)
	require.NoError(t, err)
	require.True(t, resolvedSecret.Equals(secret))
}

func buildNewSigningKeyshareCreates(t *testing.T, client *ent.Client, n int) ([]*ent.SigningKeyshareCreate, []uuid.UUID, []keys.Private) {
	t.Helper()
	creates := make([]*ent.SigningKeyshareCreate, 0, n)
	ids := make([]uuid.UUID, 0, n)
	secrets := make([]keys.Private, 0, n)
	for range n {
		id := uuid.New()
		secret := keys.GeneratePrivateKey()
		pubSource := keys.GeneratePrivateKey()
		creates = append(creates, client.SigningKeyshare.Create().
			SetID(id).
			SetStatus(st.KeyshareStatusAvailable).
			SetPublicShares(map[string]keys.Public{"1": pubSource.Public()}).
			SetPublicKey(pubSource.Public()).
			SetMinSigners(1).
			SetCoordinatorIndex(0))
		ids = append(ids, id)
		secrets = append(secrets, secret)
	}
	return creates, ids, secrets
}

// These tests exercise ent.PrepareSigningKeyshareCreatesWithSecrets directly, the same boundary the
// sibling single-key PrepareSigningKeyshareCreateWithSecret tests use. They target the secret-storage
// contract (ephemeral vs. main column, version assignment) and the main-tx rollback cleanup invariant
// for keyshare secrets — security-sensitive transaction-lifecycle behavior that the full DKG flow
// (covered end-to-end in so/grpc_test) cannot observe from its public surface.
func TestPrepareSigningKeyshareCreatesWithSecrets_UsesEphemeralAndDualWritesWhenEnabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	creates, ids, secrets := buildNewSigningKeyshareCreates(t, tc.Client, 3)
	creates, err := ent.PrepareSigningKeyshareCreatesWithSecrets(ctx, creates, ids, secrets)
	require.NoError(t, err)
	require.NoError(t, tc.Client.SigningKeyshare.CreateBulk(creates...).Exec(ctx))

	for i, id := range ids {
		created, err := tc.Client.SigningKeyshare.Get(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, created.SecretVersion)
		require.Equal(t, int32(0), *created.SecretVersion)
		require.NotNil(t, created.SecretShare)
		require.Equal(t, secrets[i], *created.SecretShare)

		ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, id, 0)
		require.NoError(t, err)
		require.True(t, ephemeralSecret.SecretShare.Equals(secrets[i]))
	}
}

func TestPrepareSigningKeyshareCreatesWithSecrets_UsesEphemeralWithoutDualWriteWhenDisabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	creates, ids, secrets := buildNewSigningKeyshareCreates(t, tc.Client, 3)
	creates, err := ent.PrepareSigningKeyshareCreatesWithSecrets(ctx, creates, ids, secrets)
	require.NoError(t, err)
	require.NoError(t, tc.Client.SigningKeyshare.CreateBulk(creates...).Exec(ctx))

	for i, id := range ids {
		created, err := tc.Client.SigningKeyshare.Get(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, created.SecretVersion)
		require.Equal(t, int32(0), *created.SecretVersion)
		require.Nil(t, created.SecretShare)

		require.NoError(t, ent.HydrateSigningKeyshareSecrets(ctx, []*ent.SigningKeyshare{created}))
		resolved, err := created.GetSecretShare(ctx)
		require.NoError(t, err)
		require.True(t, resolved.Equals(secrets[i]))
	}
}

func TestPrepareSigningKeyshareCreatesWithSecrets_MainRollbackCleansUpEphemeralVersions(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	// Build and create the keyshares through the request's tx-bound client (as the DKG store does),
	// so the rollback below actually discards the main rows.
	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	creates, ids, secrets := buildNewSigningKeyshareCreates(t, txClient, 3)
	creates, err = ent.PrepareSigningKeyshareCreatesWithSecrets(ctx, creates, ids, secrets)
	require.NoError(t, err)
	require.NoError(t, txClient.SigningKeyshare.CreateBulk(creates...).Exec(ctx))

	for i, id := range ids {
		ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, id, 0)
		require.NoError(t, err)
		require.True(t, ephemeralSecret.SecretShare.Equals(secrets[i]))
	}

	mainTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, mainTx.Rollback())

	// The main keyshares were never committed, so rollback cleanup deletes the now-orphaned
	// ephemeral versions without consulting main state.
	for _, id := range ids {
		_, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, id, 0)
		require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)
	}
}

func TestPrepareSigningKeyshareCreatesWithSecrets_NoMainTxCleansUpEphemeralVersions(t *testing.T) {
	_, tc := db.ConnectToTestPostgres(t)
	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	creates, ids, secrets := buildNewSigningKeyshareCreates(t, tc.Client, 3)
	_, err := ent.PrepareSigningKeyshareCreatesWithSecrets(ctx, creates, ids, secrets)
	require.Error(t, err)
	require.ErrorContains(t, err, "no transaction provider found in context")

	for _, id := range ids {
		_, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, id, 0)
		require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)
	}
}

func TestPrepareSigningKeyshareCreatesWithSecrets_AmbiguousCommitPreservesEphemeralVersions(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	// Dual-write off is the worst case: the secret lives only in the ephemeral store, so an
	// erroneous rollback cleanup would be unrecoverable signing-material loss.
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	creates, ids, secrets := buildNewSigningKeyshareCreates(t, txClient, 3)
	creates, err = ent.PrepareSigningKeyshareCreatesWithSecrets(ctx, creates, ids, secrets)
	require.NoError(t, err)
	require.NoError(t, txClient.SigningKeyshare.CreateBulk(creates...).Exec(ctx))

	// Simulate the post-commit rollback shape used by the middleware after a failed Commit call.
	// Once Commit has been attempted, rollback cleanup must preserve the ephemeral rows because
	// the main inserts may already have persisted.
	mainTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, mainTx.Commit())
	_ = mainTx.Rollback() // fires the cleanup hook; the driver rollback errors (already committed) and is ignored

	for i, id := range ids {
		ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, id, 0)
		require.NoError(t, err, "secret for committed keyshare %s must be preserved", id)
		require.True(t, ephemeralSecret.SecretShare.Equals(secrets[i]))
	}
}

func TestPrepareSigningKeyshareCreatesWithSecrets_CommitErrorAfterMainCommitPreservesEphemeralVersions(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	creates, ids, secrets := buildNewSigningKeyshareCreates(t, txClient, 3)
	creates, err = ent.PrepareSigningKeyshareCreatesWithSecrets(ctx, creates, ids, secrets)
	require.NoError(t, err)
	require.NoError(t, txClient.SigningKeyshare.CreateBulk(creates...).Exec(ctx))

	mainTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	mainTx.OnCommit(func(fn ent.Committer) ent.Committer {
		return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
			if err := fn.Commit(ctx, tx); err != nil {
				return err
			}
			return fmt.Errorf("forced commit hook failure after main commit")
		})
	})

	err = mainTx.Commit()
	require.ErrorContains(t, err, "forced commit hook failure")
	if tx := tc.Session.GetTxIfExists(); tx != nil {
		_ = tx.Rollback()
	}

	for i, id := range ids {
		ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, id, 0)
		require.NoError(t, err, "secret for ambiguously committed keyshare %s must be preserved", id)
		require.True(t, ephemeralSecret.SecretShare.Equals(secrets[i]))
	}
}

func TestPrepareSigningKeyshareCreatesWithSecrets_FallsBackToMainDBWhenEphemeralUnavailable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	creates, ids, secrets := buildNewSigningKeyshareCreates(t, tc.Client, 3)
	creates, err := ent.PrepareSigningKeyshareCreatesWithSecrets(ctx, creates, ids, secrets)
	require.NoError(t, err)
	require.NoError(t, tc.Client.SigningKeyshare.CreateBulk(creates...).Exec(ctx))

	for i, id := range ids {
		created, err := tc.Client.SigningKeyshare.Get(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, created.SecretShare)
		require.Equal(t, secrets[i], *created.SecretShare)
		require.Nil(t, created.SecretVersion)
	}
}

func TestHydrateSigningKeyshareSecrets_HydratesDuplicatePointersForSameID(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	secret := keys.MustParsePrivateKeyHex("d49bbd6f2e108013b7c8c9ce5e34e119cb8a7d197f4ab51b228d76c23f3f2dc4")
	version := int32(0)

	created := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, created.ID, version, secret)
	require.NoError(t, err)

	keyshareA, err := tc.Client.SigningKeyshare.Get(ctx, created.ID)
	require.NoError(t, err)
	keyshareB, err := tc.Client.SigningKeyshare.Get(ctx, created.ID)
	require.NoError(t, err)

	require.NoError(t, ent.HydrateSigningKeyshareSecrets(ctx, []*ent.SigningKeyshare{keyshareA, keyshareB}))

	resolvedA, err := keyshareA.GetSecretShare(ctx)
	require.NoError(t, err)
	require.True(t, resolvedA.Equals(secret))

	resolvedB, err := keyshareB.GetSecretShare(ctx)
	require.NoError(t, err)
	require.True(t, resolvedB.Equals(secret))
}

func TestMarkSigningKeysharesAsUsedRejectsRequestOverCapWithoutMutating(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoMaxKeysharesPerRequest: 2,
	}))

	first := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, nil)
	second := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, nil)
	third := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, nil)
	ids := []uuid.UUID{first.ID, second.ID, third.ID}

	_, err := ent.MarkSigningKeysharesAsUsed(ctx, nil, ids)
	require.ErrorContains(t, err, "keyshare request too large")

	for _, id := range ids {
		keyshare, err := tc.Client.SigningKeyshare.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, st.KeyshareStatusAvailable, keyshare.Status)
	}
}

func TestMarkSigningKeysharesAsUsedRejectsDuplicateIDsWithoutMutating(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, nil)

	_, err := ent.MarkSigningKeysharesAsUsed(ctx, nil, []uuid.UUID{keyshare.ID, keyshare.ID})
	require.ErrorContains(t, err, "duplicate keyshare id")

	persisted, err := tc.Client.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.Equal(t, st.KeyshareStatusAvailable, persisted.Status)
}

func TestMarkSigningKeysharesAsUsedAllowsEmptyIDsWithoutMutating(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, nil)

	keyshares, err := ent.MarkSigningKeysharesAsUsed(ctx, nil, nil)
	require.NoError(t, err)
	require.Empty(t, keyshares)

	persisted, err := tc.Client.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.Equal(t, st.KeyshareStatusAvailable, persisted.Status)
}

func TestUpdateSigningKeyshareWithRotatedSecret_UsesEphemeralAndDualWritesWhenEnabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	oldSecret := keys.MustParsePrivateKeyHex("31f98c9db585d9138b9083ec0d0a86a8ce4f383e1281870e7d56f2ea54f183de")
	newSecret := keys.MustParsePrivateKeyHex("53ff19722a261a55b7f67dfc6f95b5a4f95f4af6d66bdff03422ad10240cb9ed")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &oldSecret, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version, oldSecret)
	require.NoError(t, err)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(
		ctx,
		keyshare.ID,
		keyshare.SecretVersion,
		newSecret,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(1), *updated.SecretVersion)
	require.NotNil(t, updated.SecretShare)
	require.Equal(t, newSecret, *updated.SecretShare)

	ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *updated.SecretVersion)
	require.NoError(t, err)
	require.True(t, ephemeralSecret.SecretShare.Equals(newSecret))
}

func TestUpdateSigningKeyshareWithRotatedSecret_UsesEphemeralWithoutDualWriteWhenDisabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	oldSecret := keys.MustParsePrivateKeyHex("31f98c9db585d9138b9083ec0d0a86a8ce4f383e1281870e7d56f2ea54f183de")
	newSecret := keys.MustParsePrivateKeyHex("53ff19722a261a55b7f67dfc6f95b5a4f95f4af6d66bdff03422ad10240cb9ed")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &oldSecret, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version, oldSecret)
	require.NoError(t, err)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(
		ctx,
		keyshare.ID,
		keyshare.SecretVersion,
		newSecret,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(1), *updated.SecretVersion)
	require.Nil(t, updated.SecretShare)

	ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *updated.SecretVersion)
	require.NoError(t, err)
	require.True(t, ephemeralSecret.SecretShare.Equals(newSecret))

	require.NoError(t, ent.HydrateSigningKeyshareSecrets(ctx, []*ent.SigningKeyshare{updated}))
	resolvedSecret, err := updated.GetSecretShare(ctx)
	require.NoError(t, err)
	require.True(t, resolvedSecret.Equals(newSecret))
}

func TestUpdateSigningKeyshareWithRotatedSecret_MainRollbackCleansUpNewEphemeralVersion(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	oldSecret := keys.MustParsePrivateKeyHex("4b0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f89")
	newSecret := keys.MustParsePrivateKeyHex("2e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff6")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &oldSecret, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version, oldSecret)
	require.NoError(t, err)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(
		ctx,
		keyshare.ID,
		keyshare.SecretVersion,
		newSecret,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(1), *updated.SecretVersion)

	mainTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, mainTx.Rollback())

	_, err = entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, 1)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)

	oldVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, version)
	require.NoError(t, err)
	require.True(t, oldVersionSecret.SecretShare.Equals(oldSecret))
}

func TestUpdateSigningKeyshareWithRotatedSecret_MainCommitDeletesOldEphemeralVersion(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	oldSecret := keys.MustParsePrivateKeyHex("120f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f88")
	newSecret := keys.MustParsePrivateKeyHex("3e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff5")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &oldSecret, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version, oldSecret)
	require.NoError(t, err)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(ctx, keyshare.ID, keyshare.SecretVersion, newSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(1), *updated.SecretVersion)

	mainTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, mainTx.Commit())

	_, err = entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, version)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)

	newVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *updated.SecretVersion)
	require.NoError(t, err)
	require.True(t, newVersionSecret.SecretShare.Equals(newSecret))
}

func TestUpdateSigningKeyshareWithRotatedSecret_MainCommitDeletesOnlyExpectedBaseVersion(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	baseSecret := keys.MustParsePrivateKeyHex("420f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f85")
	orphanSecret := keys.MustParsePrivateKeyHex("6e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff2")
	newSecret := keys.MustParsePrivateKeyHex("4b70a54d2ac0f3e9217ec89d8a1d9de27b2f6a50e4a316f94b266e74a3bd6417")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &baseSecret, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version, baseSecret)
	require.NoError(t, err)
	_, err = entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version+1, orphanSecret)
	require.NoError(t, err)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(ctx, keyshare.ID, keyshare.SecretVersion, newSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(2), *updated.SecretVersion)

	mainTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, mainTx.Commit())

	_, err = entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, version)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)

	intermediateVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, version+1)
	require.NoError(t, err)
	require.True(t, intermediateVersionSecret.SecretShare.Equals(orphanSecret))

	newVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *updated.SecretVersion)
	require.NoError(t, err)
	require.True(t, newVersionSecret.SecretShare.Equals(newSecret))
}

// SP-3668: the ephemeral latest can sit exactly one behind the main pointer, which is the shape
// fix_keyshare exists to repair. Allocating from the ephemeral store alone lands on the base version
// the commit hook retires, so the rotation deletes the row it just wrote and reports success with the
// main pointer left dangling. The reshare path is the only rotation caller that reaches this shape:
// the others read the current secret first and fail before rotating. No sequence of public rotations
// produces this state, so the test seeds it the same way the sibling cleanup tests seed theirs.
func TestUpdateSigningKeyshareWithRotatedSecret_EphemeralLatestBehindMainPointerSurvivesCommit(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	strandedSecret := keys.MustParsePrivateKeyHex("1a0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f11")
	newSecret := keys.MustParsePrivateKeyHex("2b3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2b22e")
	mainVersion := int32(4)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &mainVersion)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, mainVersion-1, strandedSecret)
	require.NoError(t, err)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(ctx, keyshare.ID, keyshare.SecretVersion, newSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, mainVersion+1, *updated.SecretVersion, "rotation must allocate above the base version it retires")

	commitMainTxFromContext(t, ctx)

	persisted, err := tc.Client.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.SecretVersion)
	require.Equal(t, mainVersion+1, *persisted.SecretVersion)

	newVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *persisted.SecretVersion)
	require.NoError(t, err)
	require.True(t, newVersionSecret.SecretShare.Equals(newSecret), "the committed main pointer must resolve to the rotated secret")
}

// A concurrent orphan purge takes no advisory locks, so it can leave a versioned keyshare with no
// ephemeral rows at all. Allocating from the ephemeral store alone restarts at 0, regressing the
// pointer past versions the retire-below-N cleanup contract has already declared unreachable.
func TestUpdateSigningKeyshareWithRotatedSecret_MissingEphemeralRowsNeverRegressVersion(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	newSecret := keys.MustParsePrivateKeyHex("3c0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f33")
	mainVersion := int32(2)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &mainVersion)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(ctx, keyshare.ID, keyshare.SecretVersion, newSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, mainVersion+1, *updated.SecretVersion, "rotation must move the version forward from the CAS base, not restart at 0")

	commitMainTxFromContext(t, ctx)

	newVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *updated.SecretVersion)
	require.NoError(t, err)
	require.True(t, newVersionSecret.SecretShare.Equals(newSecret))
}

func TestUpdateSigningKeyshareWithRotatedSecret_CASConflictReturnsAbortedAndRollbackDeletesNewVersion(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))

	baseSecret := keys.MustParsePrivateKeyHex("520f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f84")
	winnerSecret := keys.MustParsePrivateKeyHex("7e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff1")
	loserSecret := keys.MustParsePrivateKeyHex("5b70a54d2ac0f3e9217ec89d8a1d9de27b2f6a50e4a316f94b266e74a3bd6416")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &baseSecret, &version)
	createCommittedEphemeralSecretVersion(t, ctx, tc, keyshare.ID, version, baseSecret)

	winnerCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	winner, err := ent.UpdateSigningKeyshareWithRotatedSecret(winnerCtx, keyshare.ID, keyshare.SecretVersion, winnerSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, winner.SecretVersion)
	require.Equal(t, int32(1), *winner.SecretVersion)
	commitMainTxFromContext(t, winnerCtx)

	loserCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	_, err = ent.UpdateSigningKeyshareWithRotatedSecret(loserCtx, keyshare.ID, keyshare.SecretVersion, loserSecret, nil)
	require.Error(t, err)
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.Aborted, code)
	require.Equal(t, sparkerrors.ReasonAbortedConcurrentKeyshareRotation, reason)
	rollbackMainTxFromContext(t, loserCtx)

	persisted, err := tc.Client.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.SecretVersion)
	require.Equal(t, int32(1), *persisted.SecretVersion)

	readCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	winnerVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(readCtx, keyshare.ID, 1)
	require.NoError(t, err)
	require.True(t, winnerVersionSecret.SecretShare.Equals(winnerSecret))
	_, err = entephemeral.GetSigningKeyshareSecretVersion(readCtx, keyshare.ID, 2)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)
}

func TestUpdateSigningKeyshareWithRotatedSecret_CASWithNilBase(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))

	baseSecret := keys.MustParsePrivateKeyHex("620f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f83")
	firstSecret := keys.MustParsePrivateKeyHex("8e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff0")
	secondSecret := keys.MustParsePrivateKeyHex("6b70a54d2ac0f3e9217ec89d8a1d9de27b2f6a50e4a316f94b266e74a3bd6415")

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &baseSecret, nil)

	firstCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(firstCtx, keyshare.ID, keyshare.SecretVersion, firstSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(0), *updated.SecretVersion)
	commitMainTxFromContext(t, firstCtx)

	secondCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	_, err = ent.UpdateSigningKeyshareWithRotatedSecret(secondCtx, keyshare.ID, keyshare.SecretVersion, secondSecret, nil)
	require.Error(t, err)
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.Aborted, code)
	require.Equal(t, sparkerrors.ReasonAbortedConcurrentKeyshareRotation, reason)
	rollbackMainTxFromContext(t, secondCtx)

	persisted, err := tc.Client.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.SecretVersion)
	require.Equal(t, int32(0), *persisted.SecretVersion)

	readCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	_, err = entephemeral.GetSigningKeyshareSecretVersion(readCtx, keyshare.ID, 1)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)
}

func TestUpdateSigningKeyshareWithRotatedSecret_SwallowedCASMissCommitDeletesNewVersion(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))

	baseSecret := keys.MustParsePrivateKeyHex("720f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f82")
	winnerSecret := keys.MustParsePrivateKeyHex("9e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bfef")
	loserSecret := keys.MustParsePrivateKeyHex("7b70a54d2ac0f3e9217ec89d8a1d9de27b2f6a50e4a316f94b266e74a3bd6414")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &baseSecret, &version)
	createCommittedEphemeralSecretVersion(t, ctx, tc, keyshare.ID, version, baseSecret)

	winnerCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	_, err := ent.UpdateSigningKeyshareWithRotatedSecret(winnerCtx, keyshare.ID, keyshare.SecretVersion, winnerSecret, nil)
	require.NoError(t, err)
	commitMainTxFromContext(t, winnerCtx)

	loserCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	_, err = ent.UpdateSigningKeyshareWithRotatedSecret(loserCtx, keyshare.ID, keyshare.SecretVersion, loserSecret, nil)
	require.Error(t, err)
	commitMainTxFromContext(t, loserCtx)

	readCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	_, err = entephemeral.GetSigningKeyshareSecretVersion(readCtx, keyshare.ID, 2)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)
}

func TestTweakKeyShare_StaleLoadedVersionReturnsConcurrentRotationAfterSecretCleanup(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))

	baseSecret := keys.MustParsePrivateKeyHex("2d0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f88")
	winnerSecret := keys.MustParsePrivateKeyHex("3e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff5")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)
	createCommittedEphemeralSecretVersion(t, ctx, tc, keyshare.ID, version, baseSecret)

	staleCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	staleDB, err := ent.GetDbFromContext(staleCtx)
	require.NoError(t, err)
	staleKeyshare, err := staleDB.SigningKeyshare.Get(staleCtx, keyshare.ID)
	require.NoError(t, err)

	winnerCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	winner, err := ent.UpdateSigningKeyshareWithRotatedSecret(winnerCtx, keyshare.ID, keyshare.SecretVersion, winnerSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, winner.SecretVersion)
	require.Equal(t, int32(1), *winner.SecretVersion)
	commitMainTxFromContext(t, winnerCtx)

	_, err = entephemeral.GetSigningKeyshareSecretVersion(staleCtx, keyshare.ID, version)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)

	loserTweak := keys.GeneratePrivateKey()
	_, err = staleKeyshare.TweakKeyShare(
		staleCtx,
		loserTweak,
		loserTweak.Public(),
		map[string]keys.Public{"1": loserTweak.Public()},
	)
	require.Error(t, err)
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.Aborted, code)
	require.Equal(t, sparkerrors.ReasonAbortedConcurrentKeyshareRotation, reason)
	rollbackMainTxFromContext(t, staleCtx)
}

func TestSigningKeyshareRotationConcurrency(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))

	baseSecret := keys.MustParsePrivateKeyHex("0d0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f81")
	version := int32(0)
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)
	createCommittedEphemeralSecretVersion(t, ctx, tc, keyshare.ID, version, baseSecret)

	const (
		rounds  = 4
		workers = 8
	)
	successfulTweaks := make([]keys.Private, 0, rounds)

	type rotationRequest struct {
		ctx      context.Context
		keyshare *ent.SigningKeyshare
		tweak    keys.Private
	}
	type rotationResult struct {
		tweak   keys.Private
		success bool
		err     error
	}

	for range rounds {
		requests := make([]rotationRequest, 0, workers)
		for range workers {
			requestCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
			dbClient, err := ent.GetDbFromContext(requestCtx)
			require.NoError(t, err)
			loadedKeyshare, err := dbClient.SigningKeyshare.Get(requestCtx, keyshare.ID)
			require.NoError(t, err)
			requests = append(requests, rotationRequest{
				ctx:      requestCtx,
				keyshare: loadedKeyshare,
				tweak:    keys.GeneratePrivateKey(),
			})
		}

		start := make(chan struct{})
		results := make(chan rotationResult, workers)
		var wg sync.WaitGroup
		wg.Add(len(requests))
		for _, request := range requests {
			go func() {
				defer wg.Done()
				<-start

				_, err := request.keyshare.TweakKeyShare(
					request.ctx,
					request.tweak,
					request.tweak.Public(),
					map[string]keys.Public{"1": request.tweak.Public()},
				)
				if err != nil {
					code, reason := sparkerrors.CodeAndReasonFrom(err)
					rollbackErr := rollbackMainTxFromContextNoRequire(request.ctx)
					if code == codes.Aborted && reason == sparkerrors.ReasonAbortedConcurrentKeyshareRotation {
						results <- rotationResult{err: rollbackErr}
						return
					}
					if rollbackErr != nil {
						err = fmt.Errorf("%w; rollback failed: %w", err, rollbackErr)
					}
					results <- rotationResult{err: err}
					return
				}
				if err := commitMainTxFromContextNoRequire(request.ctx); err != nil {
					results <- rotationResult{err: err}
					return
				}
				results <- rotationResult{tweak: request.tweak, success: true}
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		roundSuccesses := 0
		for result := range results {
			require.NoError(t, result.err)
			if result.success {
				roundSuccesses++
				successfulTweaks = append(successfulTweaks, result.tweak)
			}
		}
		require.Equal(t, 1, roundSuccesses)
	}

	readCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	dbClient, err := ent.GetDbFromContext(readCtx)
	require.NoError(t, err)
	persisted, err := dbClient.SigningKeyshare.Get(readCtx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.SecretVersion)

	require.NoError(t, ent.HydrateSigningKeyshareSecrets(readCtx, []*ent.SigningKeyshare{persisted}))
	finalSecret, err := persisted.GetSecretShare(readCtx)
	require.NoError(t, err)
	expectedSecret := baseSecret
	for _, tweak := range successfulTweaks {
		expectedSecret = expectedSecret.Add(tweak)
	}
	require.True(t, finalSecret.Equals(expectedSecret))

	currentSecret, err := entephemeral.GetSigningKeyshareSecretVersion(readCtx, keyshare.ID, *persisted.SecretVersion)
	require.NoError(t, err)
	require.True(t, currentSecret.SecretShare.Equals(expectedSecret))

	ephemeralDB, err := entephemeral.GetDbFromContext(readCtx)
	require.NoError(t, err)
	staleCount, err := ephemeralDB.SigningKeyshareSecret.Query().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(keyshare.ID),
			signingkeysharesecret.VersionLT(*persisted.SecretVersion),
		).
		Count(readCtx)
	require.NoError(t, err)
	require.Zero(t, staleCount)
}

func TestUpdateSigningKeyshareWithRotatedSecret_CommitErrorAfterMainCommitPreservesEphemeralVersions(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	ctx = withPostgresEphemeralSession(t, ctx, tc)

	oldSecret := keys.MustParsePrivateKeyHex("220f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f87")
	newSecret := keys.MustParsePrivateKeyHex("4e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff4")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &oldSecret, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version, oldSecret)
	require.NoError(t, err)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(ctx, keyshare.ID, keyshare.SecretVersion, newSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(1), *updated.SecretVersion)

	mainTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	mainTx.OnCommit(func(fn ent.Committer) ent.Committer {
		return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
			if err := fn.Commit(ctx, tx); err != nil {
				return err
			}
			return fmt.Errorf("forced commit hook failure after main commit")
		})
	})

	err = mainTx.Commit()
	require.ErrorContains(t, err, "forced commit hook failure")
	if tx := tc.Session.GetTxIfExists(); tx != nil {
		_ = tx.Rollback()
	}

	oldVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, version)
	require.NoError(t, err)
	require.True(t, oldVersionSecret.SecretShare.Equals(oldSecret))

	newVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *updated.SecretVersion)
	require.NoError(t, err)
	require.True(t, newVersionSecret.SecretShare.Equals(newSecret))
}

func TestUpdateSigningKeyshareWithRotatedSecret_NoMainTxDeletesNewEphemeralVersion(t *testing.T) {
	mainCtx, tc := db.ConnectToTestPostgres(t)
	ephemeralCtx := withPostgresEphemeralSession(t, t.Context(), tc)

	oldSecret := keys.MustParsePrivateKeyHex("320f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f86")
	newSecret := keys.MustParsePrivateKeyHex("5e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff3")
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, mainCtx, tc.Client, &oldSecret, &version)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ephemeralCtx, keyshare.ID, version, oldSecret)
	require.NoError(t, err)
	require.NoError(t, entephemeral.DbCommit(ephemeralCtx))

	_, err = ent.UpdateSigningKeyshareWithRotatedSecret(ephemeralCtx, keyshare.ID, keyshare.SecretVersion, newSecret, nil)
	require.Error(t, err)

	oldVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ephemeralCtx, keyshare.ID, version)
	require.NoError(t, err)
	require.True(t, oldVersionSecret.SecretShare.Equals(oldSecret))

	_, err = entephemeral.GetSigningKeyshareSecretVersion(ephemeralCtx, keyshare.ID, version+1)
	require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)
}

func withPostgresEphemeralSession(t *testing.T, ctx context.Context, tc *db.TestContext) context.Context {
	t.Helper()

	ephemeralClient := ephemeralenttest.Open(t, "postgres", tc.DatabasePath())
	t.Cleanup(func() {
		require.NoError(t, ephemeralClient.Close())
	})

	ephemeralSession := db.NewDefaultEphemeralSessionFactory(ephemeralClient).NewSession(ctx)
	t.Cleanup(func() {
		if tx := ephemeralSession.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
	})

	return entephemeral.Inject(ctx, ephemeralSession)
}

func newPostgresEphemeralRequestContext(t *testing.T, ctx context.Context, tc *db.TestContext) context.Context {
	t.Helper()

	mainSession := db.NewDefaultSessionFactory(tc.Client).NewSession(ctx)
	t.Cleanup(func() {
		if tx := mainSession.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
	})
	ctx = ent.Inject(ctx, mainSession)
	return withPostgresEphemeralSession(t, ctx, tc)
}

func createCommittedEphemeralSecretVersion(
	t *testing.T,
	ctx context.Context,
	tc *db.TestContext,
	signingKeyshareID uuid.UUID,
	version int32,
	secret keys.Private,
) {
	t.Helper()

	writeCtx := newPostgresEphemeralRequestContext(t, ctx, tc)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(writeCtx, signingKeyshareID, version, secret)
	require.NoError(t, err)
	require.NoError(t, entephemeral.DbCommit(writeCtx))
}

func commitMainTxFromContext(t *testing.T, ctx context.Context) {
	t.Helper()

	require.NoError(t, commitMainTxFromContextNoRequire(ctx))
}

func rollbackMainTxFromContext(t *testing.T, ctx context.Context) {
	t.Helper()

	require.NoError(t, rollbackMainTxFromContextNoRequire(ctx))
}

func commitMainTxFromContextNoRequire(ctx context.Context) error {
	mainTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return err
	}
	return mainTx.Commit()
}

func rollbackMainTxFromContextNoRequire(ctx context.Context) error {
	mainTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return err
	}
	return mainTx.Rollback()
}

func mustCreateSigningKeyshare(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	secret *keys.Private,
	version *int32,
) *ent.SigningKeyshare {
	t.Helper()

	publicKeySource := keys.GeneratePrivateKey()

	create := client.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{"1": publicKeySource.Public()}).
		SetPublicKey(publicKeySource.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)

	if secret != nil {
		create.SetSecretShare(*secret)
	}
	if version != nil {
		create.SetSecretVersion(*version)
	}

	keyshare, err := create.Save(ctx)
	require.NoError(t, err)
	return keyshare
}
