package ent_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entephemeral"
	ephemeralenttest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
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
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)
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

	version := int32(7)
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)

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

	oldSecret := keys.MustParsePrivateKeyHex("fd9627ee6b0fd2f6a14833ea637f5f3af8d7e4f2a5ee5ec92fae13496f95da60")
	newSecret := keys.MustParsePrivateKeyHex("ee5f45be26ef9a5fe3e29ea9d2cb4f1200519676ad958962f4f7dcae998f1a16")
	version := int32(7)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &oldSecret, &version)
	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(
		ctx,
		keyshare.ID,
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

func mustCreateSigningKeyshare(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	secret *keys.Private,
	version *int32,
) *ent.SigningKeyshare {
	t.Helper()

	publicKeySource := keys.MustParsePrivateKeyHex("e6d2b44c26c0c1b507fab0d5e66c388c5676c109b9ee41520ceba5b52e3a2a92")

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
