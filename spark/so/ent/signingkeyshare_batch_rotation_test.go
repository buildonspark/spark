package ent_test

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
)

// newBatchRotationContext returns a postgres-backed request context with main
// and ephemeral sessions plus the dual-write knob pinned to the given value.
func newBatchRotationContext(t *testing.T, dualWrite float64) (context.Context, *db.TestContext) {
	t.Helper()
	ctx, tc := db.ConnectToTestPostgres(t)
	if tc == nil {
		return nil, nil
	}
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: dualWrite,
	}))
	return withPostgresEphemeralSession(t, ctx, tc), tc
}

// createRotatableKeyshares creates n keyshares at secret version 0 with their
// v0 ephemeral rows, and a distinct tweak for each.
func createRotatableKeyshares(t *testing.T, ctx context.Context, tc *db.TestContext, n int) []*ent.SigningKeyshareTweak {
	t.Helper()
	tweaks := make([]*ent.SigningKeyshareTweak, 0, n)
	version := int32(0)
	for range n {
		secret := keys.GeneratePrivateKey()
		keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &secret, &version)
		_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, version, secret)
		require.NoError(t, err)

		shareTweak := keys.GeneratePrivateKey()
		tweaks = append(tweaks, &ent.SigningKeyshareTweak{
			Keyshare:       keyshare,
			SecretTweak:    shareTweak,
			PubKeyTweak:    shareTweak.Public(),
			PubSharesTweak: map[string]keys.Public{"1": shareTweak.Public()},
		})
	}
	return tweaks
}

func TestTweakSigningKeyshares_RotatesAllKeysharesAndCleansUpOnCommit(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 100)
	tweaks := createRotatableKeyshares(t, ctx, tc, 3)

	updated, err := ent.TweakSigningKeyshares(ctx, tweaks)
	require.NoError(t, err)
	require.Len(t, updated, 3)

	for _, tweak := range tweaks {
		got, ok := updated[tweak.Keyshare.ID]
		require.True(t, ok, "missing result for keyshare %s", tweak.Keyshare.ID)

		wantSecret := tweak.Keyshare.SecretShare.Add(tweak.SecretTweak)
		wantPubKey := tweak.Keyshare.PublicKey.Add(tweak.PubKeyTweak)
		wantShare := tweak.Keyshare.PublicShares["1"].Add(tweak.PubSharesTweak["1"])

		require.NotNil(t, got.SecretVersion)
		require.Equal(t, int32(1), *got.SecretVersion)
		require.Equal(t, wantPubKey, got.PublicKey)
		require.Equal(t, wantShare, got.PublicShares["1"])
		require.NotNil(t, got.SecretShare, "dual-write on: main secret must be set")
		require.True(t, got.SecretShare.Equals(wantSecret))

		// Persisted main row matches the returned entity.
		dbTx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		row, err := dbTx.SigningKeyshare.Get(ctx, tweak.Keyshare.ID)
		require.NoError(t, err)
		require.NotNil(t, row.SecretVersion)
		require.Equal(t, int32(1), *row.SecretVersion)
		require.Equal(t, wantPubKey, row.PublicKey)

		// Ephemeral v1 carries the rotated secret.
		ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, tweak.Keyshare.ID, 1)
		require.NoError(t, err)
		require.True(t, ephemeralSecret.SecretShare.Equals(wantSecret))
	}

	commitMainTxFromContext(t, ctx)

	// Commit retires the superseded v0 rows and keeps v1.
	for _, tweak := range tweaks {
		_, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, tweak.Keyshare.ID, 0)
		require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion)
		_, err = entephemeral.GetSigningKeyshareSecretVersion(ctx, tweak.Keyshare.ID, 1)
		require.NoError(t, err)
	}
}

func TestTweakSigningKeyshares_MainRollbackCleansUpNewEphemeralVersions(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 100)
	tweaks := createRotatableKeyshares(t, ctx, tc, 2)

	_, err := ent.TweakSigningKeyshares(ctx, tweaks)
	require.NoError(t, err)

	rollbackMainTxFromContext(t, ctx)

	for _, tweak := range tweaks {
		_, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, tweak.Keyshare.ID, 1)
		require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion, "rolled-back rotation must not leave v1 behind")
		v0, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, tweak.Keyshare.ID, 0)
		require.NoError(t, err)
		require.True(t, v0.SecretShare.Equals(*tweak.Keyshare.SecretShare))
	}
}

func TestTweakSigningKeyshares_DetectsConcurrentRotation(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 100)
	tweaks := createRotatableKeyshares(t, ctx, tc, 2)

	// First rotation wins and commits.
	_, err := ent.TweakSigningKeyshares(ctx, tweaks)
	require.NoError(t, err)
	commitMainTxFromContext(t, ctx)

	// Re-running with the now-stale entities (SecretVersion still 0) must
	// trip the per-row CAS instead of silently double-rotating.
	_, err = ent.TweakSigningKeyshares(ctx, tweaks)
	require.ErrorContains(t, err, "secret_version moved")
}

func TestTweakSigningKeyshares_MatchesSingleRowTweakKeyShare(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 100)
	tweaks := createRotatableKeyshares(t, ctx, tc, 2)

	// Rotate the first via the batch API and the second via the existing
	// single-row path with the same tweak inputs.
	batchUpdated, err := ent.TweakSigningKeyshares(ctx, tweaks[:1])
	require.NoError(t, err)
	single := tweaks[1]
	singleUpdated, err := single.Keyshare.TweakKeyShare(ctx, single.SecretTweak, single.PubKeyTweak, single.PubSharesTweak)
	require.NoError(t, err)

	batch := batchUpdated[tweaks[0].Keyshare.ID]
	require.Equal(t, *singleUpdated.SecretVersion, *batch.SecretVersion)
	require.Equal(t, tweaks[0].Keyshare.PublicKey.Add(tweaks[0].PubKeyTweak), batch.PublicKey)
	require.Equal(t, single.Keyshare.PublicKey.Add(single.PubKeyTweak), singleUpdated.PublicKey)

	// Both rotated secrets must be resolvable through the standard read path.
	require.NoError(t, ent.HydrateSigningKeyshareSecrets(ctx, []*ent.SigningKeyshare{batch, singleUpdated}))
	batchSecret, err := batch.GetSecretShare(ctx)
	require.NoError(t, err)
	require.True(t, batchSecret.Equals(tweaks[0].Keyshare.SecretShare.Add(tweaks[0].SecretTweak)))
}

// The riskiest batch-specific path: an earlier item wins its CAS before a
// later item loses in the same call. The batch must error, and the caller's
// rollback must purge every new ephemeral version while leaving every base
// version (and main row) untouched.
func TestTweakSigningKeyshares_MixedOutcomeBatchRollsBackCleanly(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 100)
	tweaks := createRotatableKeyshares(t, ctx, tc, 2)
	fresh, stale := tweaks[0], tweaks[1]

	// Rotate the second keyshare out from under its entity within the same
	// tx: its row moves to secret_version 1 while stale.Keyshare still says 0.
	_, err := stale.Keyshare.TweakKeyShare(ctx, keys.GeneratePrivateKey(), stale.PubKeyTweak, stale.PubSharesTweak)
	require.NoError(t, err)

	// fresh wins its CAS first, then stale loses; the whole batch errors.
	_, err = ent.TweakSigningKeyshares(ctx, []*ent.SigningKeyshareTweak{fresh, stale})
	require.ErrorContains(t, err, "secret_version moved")

	rollbackMainTxFromContext(t, ctx)

	for _, tweak := range tweaks {
		// Every version above the base was created inside the rolled-back tx
		// (the out-of-band single-row rotation included) and must be purged.
		for version := int32(1); version <= 2; version++ {
			_, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, tweak.Keyshare.ID, version)
			require.ErrorIs(t, err, entephemeral.ErrNoSecretVersion, "keyshare %s version %d must be purged", tweak.Keyshare.ID, version)
		}
		v0, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, tweak.Keyshare.ID, 0)
		require.NoError(t, err)
		require.True(t, v0.SecretShare.Equals(*tweak.Keyshare.SecretShare))

		// Main rows are back at the base version with the original key material.
		row, err := tc.Client.SigningKeyshare.Get(ctx, tweak.Keyshare.ID)
		require.NoError(t, err)
		require.NotNil(t, row.SecretVersion)
		require.Equal(t, int32(0), *row.SecretVersion)
		require.Equal(t, tweak.Keyshare.PublicKey, row.PublicKey)
	}
}

// A concurrent orphan purge (which takes no advisory locks) can delete a
// keyshare's ephemeral rows between reads. The rotation must never restart a
// versioned keyshare at 0 — the new version must stay above the CAS base even
// when no ephemeral row is found.
func TestTweakSigningKeyshares_MissingEphemeralRowsNeverRegressVersion(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 100)

	secret := keys.GeneratePrivateKey()
	base := int32(2)
	// Main row says version 2; the ephemeral store has no rows at all (as if
	// everything below the pointer was purged and the current row raced away).
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &secret, &base)
	shareTweak := keys.GeneratePrivateKey()

	updated, err := ent.TweakSigningKeyshares(ctx, []*ent.SigningKeyshareTweak{{
		Keyshare:       keyshare,
		SecretTweak:    shareTweak,
		PubKeyTweak:    shareTweak.Public(),
		PubSharesTweak: map[string]keys.Public{"1": shareTweak.Public()},
	}})
	require.NoError(t, err)

	got := updated[keyshare.ID]
	require.NotNil(t, got.SecretVersion)
	require.Equal(t, base+1, *got.SecretVersion, "rotation must move the version forward from the CAS base, not restart at 0")

	ephemeralSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, base+1)
	require.NoError(t, err)
	require.True(t, ephemeralSecret.SecretShare.Equals(secret.Add(shareTweak)))
}

// The batch path shares the single-row version allocator, so it inherits the SP-3668 guarantee: a
// keyshare whose ephemeral latest is one behind the main pointer must not allocate the base version
// its commit hook retires. Reaching the allocator at all requires the legacy main secret column to
// still hold the current secret — otherwise the pre-rotation read fails on the missing ephemeral row
// before any version is allocated, which is why the reshare path is the only caller exposed in prod.
func TestTweakSigningKeyshares_EphemeralLatestBehindMainPointerSurvivesCommit(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 0)

	secret := keys.GeneratePrivateKey()
	mainVersion := int32(4)
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &secret, &mainVersion)
	_, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, keyshare.ID, mainVersion-1, keys.GeneratePrivateKey())
	require.NoError(t, err)

	shareTweak := keys.GeneratePrivateKey()
	updated, err := ent.TweakSigningKeyshares(ctx, []*ent.SigningKeyshareTweak{{
		Keyshare:       keyshare,
		SecretTweak:    shareTweak,
		PubKeyTweak:    shareTweak.Public(),
		PubSharesTweak: map[string]keys.Public{"1": shareTweak.Public()},
	}})
	require.NoError(t, err)

	got := updated[keyshare.ID]
	require.NotNil(t, got.SecretVersion)
	require.Equal(t, mainVersion+1, *got.SecretVersion, "rotation must allocate above the base version it retires")

	commitMainTxFromContext(t, ctx)

	persisted, err := tc.Client.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.SecretVersion)
	require.Equal(t, mainVersion+1, *persisted.SecretVersion)

	newVersionSecret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, keyshare.ID, *persisted.SecretVersion)
	require.NoError(t, err)
	require.True(t, newVersionSecret.SecretShare.Equals(secret.Add(shareTweak)), "the committed main pointer must resolve to the rotated secret")
}

func TestTweakSigningKeyshares_RejectsDuplicateKeyshares(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 100)
	tweaks := createRotatableKeyshares(t, ctx, tc, 1)

	_, err := ent.TweakSigningKeyshares(ctx, []*ent.SigningKeyshareTweak{tweaks[0], tweaks[0]})
	require.ErrorContains(t, err, "duplicate")
}

func TestTweakSigningKeyshares_EmptyInputIsNoOp(t *testing.T) {
	ctx, _ := newBatchRotationContext(t, 100)

	updated, err := ent.TweakSigningKeyshares(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, updated)
}

func TestTweakSigningKeyshares_FallsBackToMainDBWhenEphemeralUnavailable(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	if tc == nil {
		return
	}
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 0,
	}))
	// No ephemeral session injected: rotation must fall back to a main-DB-only
	// write that always persists the secret and clears the version pointer.
	secret := keys.GeneratePrivateKey()
	version := int32(0)
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &secret, &version)
	shareTweak := keys.GeneratePrivateKey()

	updated, err := ent.TweakSigningKeyshares(ctx, []*ent.SigningKeyshareTweak{{
		Keyshare:       keyshare,
		SecretTweak:    shareTweak,
		PubKeyTweak:    shareTweak.Public(),
		PubSharesTweak: map[string]keys.Public{"1": shareTweak.Public()},
	}})
	require.NoError(t, err)

	got := updated[keyshare.ID]
	require.Nil(t, got.SecretVersion, "legacy fallback clears the version pointer")
	require.NotNil(t, got.SecretShare)
	require.True(t, got.SecretShare.Equals(secret.Add(shareTweak)))
}

func TestTweakSigningKeyshares_WithoutDualWriteClearsMainSecret(t *testing.T) {
	ctx, tc := newBatchRotationContext(t, 0)
	tweaks := createRotatableKeyshares(t, ctx, tc, 1)

	updated, err := ent.TweakSigningKeyshares(ctx, tweaks)
	require.NoError(t, err)

	got := updated[tweaks[0].Keyshare.ID]
	require.Nil(t, got.SecretShare, "dual-write off: main secret column must be cleared")
	require.NotNil(t, got.SecretVersion)
	require.Equal(t, int32(1), *got.SecretVersion)

	require.NoError(t, ent.HydrateSigningKeyshareSecrets(ctx, []*ent.SigningKeyshare{got}))
	resolved, err := got.GetSecretShare(ctx)
	require.NoError(t, err)
	require.True(t, resolved.Equals(tweaks[0].Keyshare.SecretShare.Add(tweaks[0].SecretTweak)))
}
