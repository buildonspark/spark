package task

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/entephemeral"
	entephemeraltest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestBackfillSigningKeyshareSecrets_BackfillsMissingSecretVersion(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)
	secret := keys.GeneratePrivateKey()
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &secret, nil)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, backfillSigningKeyshareSecretVersion, *updated.SecretVersion)
	require.NotNil(t, updated.SecretShare)
	require.True(t, updated.SecretShare.Equals(secret))

	ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(
		entephemeral.Inject(ctx, db.NewReadOnlyEphemeralSession(ctx, ephemeralClient)),
		keyshare.ID,
		backfillSigningKeyshareSecretVersion,
	)
	require.NoError(t, err)
	require.True(t, ephemeralSecret.SecretShare.Equals(secret))
}

func TestBackfillSigningKeyshareSecrets_DisabledKnobNoOps(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, nil, nil)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 0,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.Nil(t, updated.SecretVersion)

	count, err := ephemeralClient.SigningKeyshareSecret.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestBackfillSigningKeyshareSecrets_ExistingMatchingEphemeralSecret(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)
	secret := keys.GeneratePrivateKey()
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &secret, nil)
	_, err := ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetVersion(backfillSigningKeyshareSecretVersion).
		SetSecretShare(secret).
		Save(ctx)
	require.NoError(t, err)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err = task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, backfillSigningKeyshareSecretVersion, *updated.SecretVersion)
	count, err := ephemeralClient.SigningKeyshareSecret.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestBackfillSigningKeyshareSecrets_MismatchedEphemeralSecretIsSkipped(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)

	// Mismatched row is created first so its UUIDv7 sorts ahead of the healthy row
	// in the batch's ByID(asc) order — this is the head-of-line case the skip
	// behavior is designed to handle.
	mismatchedSecret := keys.GeneratePrivateKey()
	mismatchedKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &mismatchedSecret, nil)
	_, err := ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(mismatchedKeyshare.ID).
		SetVersion(backfillSigningKeyshareSecretVersion).
		SetSecretShare(keys.GeneratePrivateKey()).
		Save(ctx)
	require.NoError(t, err)

	healthySecret := keys.GeneratePrivateKey()
	healthyKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &healthySecret, nil)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err = task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.NoError(t, err)

	mismatchedAfter, err := mainClient.SigningKeyshare.Get(ctx, mismatchedKeyshare.ID)
	require.NoError(t, err)
	require.Nil(t, mismatchedAfter.SecretVersion)

	healthyAfter, err := mainClient.SigningKeyshare.Get(ctx, healthyKeyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, healthyAfter.SecretVersion)
	require.Equal(t, backfillSigningKeyshareSecretVersion, *healthyAfter.SecretVersion)
}

func TestBackfillSigningKeyshareSecrets_CursorAdvancesPastFullBatchOfSkippedRows(t *testing.T) {
	// Without a cursor, a full batch of persistently failing rows would be re-selected
	// on every iteration (their secret_version stays nil, so they remain eligible). This
	// test fills the first batch with mismatched rows and verifies a healthy row at a
	// higher ID still gets backfilled within the same run.
	originalBatchSize := backfillSigningKeyshareSecretsBatchSize
	backfillSigningKeyshareSecretsBatchSize = 2
	t.Cleanup(func() { backfillSigningKeyshareSecretsBatchSize = originalBatchSize })

	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)

	for range 3 {
		secret := keys.GeneratePrivateKey()
		keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &secret, nil)
		_, err := ephemeralClient.SigningKeyshareSecret.Create().
			SetSigningKeyshareID(keyshare.ID).
			SetVersion(backfillSigningKeyshareSecretVersion).
			SetSecretShare(keys.GeneratePrivateKey()).
			Save(ctx)
		require.NoError(t, err)
	}
	healthySecret := keys.GeneratePrivateKey()
	healthyKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &healthySecret, nil)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.NoError(t, err)

	healthyAfter, err := mainClient.SigningKeyshare.Get(ctx, healthyKeyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, healthyAfter.SecretVersion)
	require.Equal(t, backfillSigningKeyshareSecretVersion, *healthyAfter.SecretVersion)
}

func TestBackfillSigningKeyshareSecrets_ErrorsWhenEnabledWithoutEphemeralDB(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err := task.RunOnce(ctx, cfg, sessionCtx.Client, nil, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.ErrorContains(t, err, "ephemeral db is required")
}

func TestBackfillSigningKeyshareSecrets_IgnoresAlreadyVersionedKeyshares(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)
	version := int32(12)
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, nil, &version)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, version, *updated.SecretVersion)
	count, err := ephemeralClient.SigningKeyshareSecret.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestBackfillSigningKeyshareSecrets_SkipsRowsWithNoMainSecret(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)
	missingSecretKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, nil, nil)
	validSecret := keys.GeneratePrivateKey()
	validKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &validSecret, nil)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.NoError(t, err)

	missingSecretAfter, err := mainClient.SigningKeyshare.Get(ctx, missingSecretKeyshare.ID)
	require.NoError(t, err)
	require.Nil(t, missingSecretAfter.SecretVersion)

	validAfter, err := mainClient.SigningKeyshare.Get(ctx, validKeyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, validAfter.SecretVersion)
	require.Equal(t, backfillSigningKeyshareSecretVersion, *validAfter.SecretVersion)
}

func TestBackfillSigningKeyshareSecrets_SkipsLockedRows(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)
	lockedSecret := keys.GeneratePrivateKey()
	unlockedSecret := keys.GeneratePrivateKey()
	lockedKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &lockedSecret, nil)
	unlockedKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &unlockedSecret, nil)

	lockTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockTx.Rollback() })
	_, err = lockTx.SigningKeyshare.Query().
		Where(signingkeyshare.IDEQ(lockedKeyshare.ID)).
		ForUpdate().
		Only(ctx)
	require.NoError(t, err)

	task := getBackfillSigningKeyshareSecretsTask(t)
	err = task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoBackfillSigningKeyshareSecretsEnabled: 100,
	}))
	require.NoError(t, err)

	lockedAfter, err := mainClient.SigningKeyshare.Get(ctx, lockedKeyshare.ID)
	require.NoError(t, err)
	require.Nil(t, lockedAfter.SecretVersion)
	unlockedAfter, err := mainClient.SigningKeyshare.Get(ctx, unlockedKeyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, unlockedAfter.SecretVersion)
	require.Equal(t, backfillSigningKeyshareSecretVersion, *unlockedAfter.SecretVersion)
}

func newBackfillSigningKeyshareSecretsContext(t *testing.T) (context.Context, *ent.Client, *entephemeral.Client) {
	t.Helper()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	mainClient := sessionCtx.Client
	ephemeralClient := entephemeraltest.Open(t, "postgres", sessionCtx.DatabasePath())

	t.Cleanup(func() {
		require.NoError(t, ephemeralClient.Close())
	})

	return ctx, mainClient, ephemeralClient
}

func createBackfillSigningKeyshare(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	secret *keys.Private,
	secretVersion *int32,
) *ent.SigningKeyshare {
	t.Helper()

	publicSecret := keys.GeneratePrivateKey()
	if secret != nil {
		publicSecret = *secret
	}
	create := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(publicSecret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)
	if secret != nil {
		create = create.SetSecretShare(*secret)
	}
	if secretVersion != nil {
		create = create.SetSecretVersion(*secretVersion)
	}

	keyshare, err := create.Save(ctx)
	require.NoError(t, err)
	return keyshare
}

func getBackfillSigningKeyshareSecretsTask(t *testing.T) ScheduledTaskSpec {
	t.Helper()
	for _, task := range AllScheduledTasks() {
		if task.Name == "backfill_signing_keyshare_secrets" {
			return task
		}
	}
	t.Fatal("scheduled task not found: backfill_signing_keyshare_secrets")
	return ScheduledTaskSpec{}
}
