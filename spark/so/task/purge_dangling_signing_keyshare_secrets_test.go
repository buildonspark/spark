package task

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entephemeral"
	entephemeraltest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestPurgeDanglingSigningKeyshareSecrets_DeletesSupersededOldVersion(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	keyshareID := createMainSigningKeyshare(t, ctx, mainClient, new(int32(2)))
	createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 1, now.Add(-20*time.Minute))
	activeSecretID := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 2, now.Add(-15*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 2, result.CandidateCount)
	require.Equal(t, 1, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	existingIDs, err := ephemeralClient.SigningKeyshareSecret.Query().IDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{activeSecretID}, existingIDs)
}

func TestPurgeDanglingSigningKeyshareSecrets_PreservesCurrentlyReferencedVersion(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	keyshareID := createMainSigningKeyshare(t, ctx, mainClient, new(int32(5)))
	createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 4, now.Add(-30*time.Minute))
	activeSecretID := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 5, now.Add(-25*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 2, result.CandidateCount)
	require.Equal(t, 1, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	remaining, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(keyshareID)).
		IDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{activeSecretID}, remaining)
}

func TestPurgeDanglingSigningKeyshareSecrets_AllCandidatesAreActive_NoDeletes(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	keyshareID := createMainSigningKeyshare(t, ctx, mainClient, new(int32(7)))
	activeSecretID := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 7, now.Add(-20*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.CandidateCount)
	require.Equal(t, 0, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	remaining, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(keyshareID)).
		IDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{activeSecretID}, remaining)
}

func TestPurgeDanglingSigningKeyshareSecrets_DeletesLoneOrphan(t *testing.T) {
	t.Parallel()
	ctx, _, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, uuid.New(), 1, now.Add(-20*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.CandidateCount)
	require.Equal(t, 1, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	count, err := ephemeralClient.SigningKeyshareSecret.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestPurgeDanglingSigningKeyshareSecrets_DeletesWhenMainSecretVersionIsNil(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	keyshareID := createMainSigningKeyshare(t, ctx, mainClient, nil)
	createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 1, now.Add(-20*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.CandidateCount)
	require.Equal(t, 1, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	count, err := ephemeralClient.SigningKeyshareSecret.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestPurgeDanglingSigningKeyshareSecrets_PreservesUnreferencedNewVersionBeforeGracePeriod(t *testing.T) {
	t.Parallel()
	ctx, _, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	secretID := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, uuid.New(), 1, now.Add(-9*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 0, result.CandidateCount)
	require.Equal(t, 0, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	existingIDs, err := ephemeralClient.SigningKeyshareSecret.Query().IDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{secretID}, existingIDs)
}

func TestPurgeDanglingSigningKeyshareSecrets_DeletesUnreferencedVersionAfterGracePeriod(t *testing.T) {
	t.Parallel()
	ctx, _, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)

	createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, uuid.New(), 1, now.Add(-9*time.Minute))

	cutoffID := uuids.UUIDv7FromTime(now.Add(-8 * time.Minute))
	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.CandidateCount)
	require.Equal(t, 1, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	count, err := ephemeralClient.SigningKeyshareSecret.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestPurgeDanglingSigningKeyshareSecrets_PreservesAgedRowsWhenMainPointsToMissingVersion(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	keyshareID := createMainSigningKeyshare(t, ctx, mainClient, new(int32(9)))
	staleSecretID1 := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 1, now.Add(-20*time.Minute))
	staleSecretID2 := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, 2, now.Add(-15*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, purgeDanglingSigningKeyshareSecretsDefaultBatchSize, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 2, result.CandidateCount)
	require.Equal(t, 0, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	existingIDs, err := ephemeralClient.SigningKeyshareSecret.Query().IDs(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{staleSecretID1, staleSecretID2}, existingIDs)
}

func TestPurgeDanglingSigningKeyshareSecrets_ScansPastActivePrefixToFindDanglingRows(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	for i := range 3 {
		activeVersion := int32(0)
		keyshareID := createMainSigningKeyshare(t, ctx, mainClient, &activeVersion)
		createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, activeVersion, now.Add(time.Duration(-30+i)*time.Minute))
	}

	danglingSecretID := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, uuid.New(), 99, now.Add(-20*time.Minute))

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, 2, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 4, result.CandidateCount)
	require.Equal(t, 1, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor)

	existingIDs, err := ephemeralClient.SigningKeyshareSecret.Query().IDs(ctx)
	require.NoError(t, err)
	require.NotContains(t, existingIDs, danglingSecretID)
}

func TestPurgeDanglingSigningKeyshareSecrets_FillsDeleteBatchAcrossMultiplePages(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	for i := range 3 {
		activeVersion := int32(0)
		keyshareID := createMainSigningKeyshare(t, ctx, mainClient, &activeVersion)
		createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, activeVersion, now.Add(time.Duration(-30+i)*time.Minute))
	}

	danglingKeyshareID := uuid.New()
	for i := range 3 {
		createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, danglingKeyshareID, int32(100+i), now.Add(time.Duration(-20+i)*time.Minute))
	}

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, 2, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 6, result.CandidateCount)
	require.Equal(t, 2, result.DeletedCount)
	require.True(t, result.FoundFullDeleteBatch)
	require.NotNil(t, result.NextCursor)

	remainingDanglingCount, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(danglingKeyshareID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, remainingDanglingCount)
}

// When the final page of aged candidates is short AND the cumulative delete
// list reaches batchSize on that page, the scan has both filled a full batch
// and reached the end of the aged data. The cursor should reset.
func TestPurgeDanglingSigningKeyshareSecrets_FullDeleteBatchOnFinalShortPageResetsCursor(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	for i := range 3 {
		activeVersion := int32(0)
		keyshareID := createMainSigningKeyshare(t, ctx, mainClient, &activeVersion)
		createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, activeVersion, now.Add(time.Duration(-30+i)*time.Minute))
	}

	danglingKeyshareID := uuid.New()
	for i := range 2 {
		createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, danglingKeyshareID, int32(100+i), now.Add(time.Duration(-20+i)*time.Minute))
	}

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, 2, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, 2, result.DeletedCount)
	require.True(t, result.FoundFullDeleteBatch)
	require.Nil(t, result.NextCursor, "cursor should reset when the page that fills the delete batch is also the last page")

	remainingDanglingCount, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(danglingKeyshareID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, remainingDanglingCount)
}

// SP-3095 regression: when the per-run scan budget is exhausted before a
// dangling row is reached, the cursor advances so the next run picks up where
// this one left off and eventually finds it. Together these two invocations
// reproduce what happens across two scheduled runs.
func TestPurgeDanglingSigningKeyshareSecrets_CursorAdvancesUntilDanglingRowFound(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	batchSize := 2
	maxScanCount := 4
	for i := range maxScanCount {
		activeVersion := int32(0)
		keyshareID := createMainSigningKeyshare(t, ctx, mainClient, &activeVersion)
		createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, activeVersion, now.Add(time.Duration(-40+i)*time.Minute))
	}
	danglingSecretID := createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, uuid.New(), 77, now.Add(-15*time.Minute))

	firstResult, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, batchSize, maxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, maxScanCount, firstResult.CandidateCount)
	require.Equal(t, 0, firstResult.DeletedCount)
	require.NotNil(t, firstResult.NextCursor, "cursor should advance when budget is exhausted mid-table")

	existingIDs, err := ephemeralClient.SigningKeyshareSecret.Query().IDs(ctx)
	require.NoError(t, err)
	require.Contains(t, existingIDs, danglingSecretID)

	secondResult, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, batchSize, maxScanCount, firstResult.NextCursor)
	require.NoError(t, err)
	require.Equal(t, 1, secondResult.DeletedCount)
	require.Nil(t, secondResult.NextCursor, "cursor should reset once the scan reaches the end of the aged data")

	existingIDs, err = ephemeralClient.SigningKeyshareSecret.Query().IDs(ctx)
	require.NoError(t, err)
	require.NotContains(t, existingIDs, danglingSecretID)
}

// A run that hits its scan budget without finishing should advance the cursor
// rather than reset it.
func TestPurgeDanglingSigningKeyshareSecrets_BudgetExhaustionAdvancesCursor(t *testing.T) {
	t.Parallel()
	ctx, mainClient, ephemeralClient := newPurgeDanglingSigningKeyshareSecretsContext(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	cutoffID := uuids.UUIDv7FromTime(now.Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))

	maxScanCount := 4
	for i := range maxScanCount + 5 {
		activeVersion := int32(0)
		keyshareID := createMainSigningKeyshare(t, ctx, mainClient, &activeVersion)
		createEphemeralSigningKeyshareSecret(t, ctx, ephemeralClient, keyshareID, activeVersion, now.Add(time.Duration(-40+i)*time.Minute))
	}

	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, 2, maxScanCount, nil)
	require.NoError(t, err)
	require.Equal(t, maxScanCount, result.CandidateCount)
	require.Equal(t, 0, result.DeletedCount)
	require.False(t, result.FoundFullDeleteBatch)
	require.NotNil(t, result.NextCursor)
}

func TestPurgeDanglingSigningKeyshareSecrets_NoOpWithoutEphemeralSession(t *testing.T) {
	t.Parallel()
	mainClient := db.NewTestSQLiteClient(t)
	cfg := sparktesting.TestConfig(t)

	purgeTask := getScheduledTaskByName(t, "purge_dangling_signing_keyshare_secrets")
	err := purgeTask.RunOnce(t.Context(), cfg, mainClient, nil, knobs.NewEmptyFixedKnobs())
	require.NoError(t, err)
}

func newPurgeDanglingSigningKeyshareSecretsContext(t *testing.T) (context.Context, *ent.Client, *entephemeral.Client) {
	t.Helper()

	mainClient := db.NewTestSQLiteClient(t)
	ephemeralClient := entephemeraltest.Open(t, "sqlite3", fmt.Sprintf(
		"file:%s?mode=memory&_fk=1",
		strings.ReplaceAll(t.Name(), "/", "_"),
	))

	t.Cleanup(func() {
		require.NoError(t, ephemeralClient.Close())
		require.NoError(t, mainClient.Close())
	})

	ctx := ent.Inject(t.Context(), db.NewReadOnlySession(t.Context(), mainClient))
	ctx = entephemeral.Inject(ctx, db.NewReadOnlyEphemeralSession(t.Context(), ephemeralClient))
	return ctx, mainClient, ephemeralClient
}

func createMainSigningKeyshare(t *testing.T, ctx context.Context, client *ent.Client, secretVersion *int32) uuid.UUID {
	t.Helper()

	secret := keys.GeneratePrivateKey()
	create := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)
	if secretVersion != nil {
		create = create.SetSecretVersion(*secretVersion)
	}

	row, err := create.Save(ctx)
	require.NoError(t, err)
	return row.ID
}

func createEphemeralSigningKeyshareSecret(
	t *testing.T,
	ctx context.Context,
	client *entephemeral.Client,
	signingKeyshareID uuid.UUID,
	version int32,
	ts time.Time,
) uuid.UUID {
	t.Helper()

	secretID := uuids.UUIDv7FromTime(ts)
	_, err := client.SigningKeyshareSecret.Create().
		SetID(secretID).
		SetSigningKeyshareID(signingKeyshareID).
		SetVersion(version).
		SetSecretShare(keys.GeneratePrivateKey()).
		Save(ctx)
	require.NoError(t, err)
	return secretID
}

func getScheduledTaskByName(t *testing.T, name string) ScheduledTaskSpec {
	t.Helper()
	for _, task := range AllScheduledTasks() {
		if task.Name == name {
			return task
		}
	}
	t.Fatalf("scheduled task not found: %s", name)
	return ScheduledTaskSpec{}
}
