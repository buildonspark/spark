package task

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestClearSigningKeyshareSecretShares_ClearsSharesWithVersion(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)

	version := int32(0)
	secret := keys.GeneratePrivateKey()
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &secret, &version)

	task := getClearSigningKeyshareSecretSharesTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoClearSigningKeyshareSecretSharesEnabled: 100,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.Nil(t, updated.SecretShare)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, version, *updated.SecretVersion)
}

func TestClearSigningKeyshareSecretShares_DisabledKnobNoOps(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)

	version := int32(0)
	secret := keys.GeneratePrivateKey()
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &secret, &version)

	task := getClearSigningKeyshareSecretSharesTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoClearSigningKeyshareSecretSharesEnabled: 0,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretShare)
	require.True(t, updated.SecretShare.Equals(secret))
}

func TestClearSigningKeyshareSecretShares_IgnoresKeysharesWithNullVersion(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)

	// Un-backfilled keyshare: secret_share NOT NULL, secret_version NULL.
	// Clearing this row would strand its secret with no ephemeral fallback — the
	// cron must leave it alone. This is the load-bearing safety invariant.
	secret := keys.GeneratePrivateKey()
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &secret, nil)

	task := getClearSigningKeyshareSecretSharesTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoClearSigningKeyshareSecretSharesEnabled: 100,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretShare)
	require.True(t, updated.SecretShare.Equals(secret))
	require.Nil(t, updated.SecretVersion)
}

func TestClearSigningKeyshareSecretShares_IgnoresKeysharesWithNullShare(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)

	// Post-flip rotation result: secret_share already NULL, secret_version set.
	// The cron should be a no-op and the row should be left exactly as-is.
	version := int32(5)
	keyshare := createBackfillSigningKeyshare(t, ctx, mainClient, nil, &version)

	task := getClearSigningKeyshareSecretSharesTask(t)
	err := task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoClearSigningKeyshareSecretSharesEnabled: 100,
	}))
	require.NoError(t, err)

	updated, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.Nil(t, updated.SecretShare)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, version, *updated.SecretVersion)
}

// SkipsLockedRows guards the concurrency contract: a row locked by a concurrent
// transaction (e.g. an in-flight rotation) must not be touched by the cron. This
// is invisible from the public boundary because correctness of "doesn't fight a
// rotation for the lock" only matters under concurrency, which the public API
// can't easily exercise on its own.
func TestClearSigningKeyshareSecretShares_SkipsLockedRows(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newBackfillSigningKeyshareSecretsContext(t)
	cfg := sparktesting.TestConfig(t)

	version := int32(0)
	lockedSecret := keys.GeneratePrivateKey()
	unlockedSecret := keys.GeneratePrivateKey()
	lockedKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &lockedSecret, &version)
	unlockedKeyshare := createBackfillSigningKeyshare(t, ctx, mainClient, &unlockedSecret, &version)

	lockTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockTx.Rollback() })
	_, err = lockTx.SigningKeyshare.Query().
		Where(signingkeyshare.IDEQ(lockedKeyshare.ID)).
		ForUpdate().
		Only(ctx)
	require.NoError(t, err)

	task := getClearSigningKeyshareSecretSharesTask(t)
	err = task.RunOnce(ctx, cfg, mainClient, ephemeralClient, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoClearSigningKeyshareSecretSharesEnabled: 100,
	}))
	require.NoError(t, err)

	lockedAfter, err := mainClient.SigningKeyshare.Get(ctx, lockedKeyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, lockedAfter.SecretShare)
	require.True(t, lockedAfter.SecretShare.Equals(lockedSecret))

	unlockedAfter, err := mainClient.SigningKeyshare.Get(ctx, unlockedKeyshare.ID)
	require.NoError(t, err)
	require.Nil(t, unlockedAfter.SecretShare)
}

func getClearSigningKeyshareSecretSharesTask(t *testing.T) ScheduledTaskSpec {
	t.Helper()
	for _, task := range AllScheduledTasks() {
		if task.Name == "clear_signing_keyshare_secret_shares" {
			return task
		}
	}
	t.Fatal("scheduled task not found: clear_signing_keyshare_secret_shares")
	return ScheduledTaskSpec{}
}
