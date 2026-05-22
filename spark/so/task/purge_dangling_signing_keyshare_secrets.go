package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uuids"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
	"github.com/lightsparkdev/spark/so/knobs"
)

// purgeDanglingSigningKeyshareSecretsMu serialises scheduled ticks within a
// single SO process so a slow run (e.g. a full 100k-row scan) can't overlap
// the next one and clobber its cursor write.
var purgeDanglingSigningKeyshareSecretsMu sync.Mutex

const (
	purgeDanglingSigningKeyshareSecretsGracePeriod         = 10 * time.Minute
	purgeDanglingSigningKeyshareSecretsDefaultBatchSize    = 1000
	purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount = 100_000
	purgeDanglingSigningKeyshareSecretsCursorKeyPrefix     = "purge_dangling_signing_keyshare_secrets_cursor"
	// Cursor TTL exceeds the run interval by a wide margin so a transient
	// memcache outage doesn't reset progress on a healthy table. Loss of the
	// cursor is harmless — the next run just restarts at the oldest row.
	purgeDanglingSigningKeyshareSecretsCursorTTL = 7 * 24 * 3600
)

type purgeDanglingSigningKeyshareSecretsBatchResult struct {
	CandidateCount       int
	DeletedCount         int
	FoundFullDeleteBatch bool
	// NextCursor is the id to resume from on the next run, or nil if the
	// scan reached the end of the aged data and should wrap to the oldest
	// row.
	NextCursor *uuid.UUID
}

type purgeDanglingSigningKeyshareSecretsCollectionResult struct {
	CandidateCount       int
	FoundFullDeleteBatch bool
	NextCursor           *uuid.UUID
}

// runPurgeDanglingSigningKeyshareSecrets is invoked by the scheduled task. It
// loads the persisted scan cursor from memcache, runs one bounded batch, and
// writes the new cursor back. The cursor wraps to the oldest row whenever a
// run reaches the end of the aged data.
func runPurgeDanglingSigningKeyshareSecrets(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
	logger := logging.GetLoggerFromContext(ctx)
	sugar := logger.Sugar()

	if !purgeDanglingSigningKeyshareSecretsMu.TryLock() {
		sugar.Info("purge_dangling_signing_keyshare_secrets: previous tick still running on this pod, skipping")
		return nil
	}
	defer purgeDanglingSigningKeyshareSecretsMu.Unlock()

	batchSize := int(knobsService.GetValue(knobs.KnobPurgeDanglingSigningKeyshareSecretsBatchSize, purgeDanglingSigningKeyshareSecretsDefaultBatchSize))
	if batchSize <= 0 {
		sugar.Warnf("purge_dangling_signing_keyshare_secrets: invalid batchSize %d (knob %s), skipping run", batchSize, knobs.KnobPurgeDanglingSigningKeyshareSecretsBatchSize)
		return nil
	}
	maxScanCount := int(knobsService.GetValue(knobs.KnobPurgeDanglingSigningKeyshareSecretsMaxScanCount, purgeDanglingSigningKeyshareSecretsDefaultMaxScanCount))
	if maxScanCount <= 0 {
		sugar.Warnf("purge_dangling_signing_keyshare_secrets: invalid maxScanCount %d (knob %s), skipping run", maxScanCount, knobs.KnobPurgeDanglingSigningKeyshareSecretsMaxScanCount)
		return nil
	}

	var mc *memcache.Client
	if config.CacheURI != "" {
		mc = newPurgeDanglingSigningKeyshareSecretsMemcacheClient(config.CacheURI)
	}
	cursorKey := purgeDanglingSigningKeyshareSecretsCursorKey(config.Index)
	startCursor := loadPurgeDanglingSigningKeyshareSecretsCursor(mc, cursorKey)

	cutoffID := uuids.UUIDv7FromTime(time.Now().Add(-purgeDanglingSigningKeyshareSecretsGracePeriod))
	result, err := purgeDanglingSigningKeyshareSecretsBatch(ctx, cutoffID, batchSize, maxScanCount, startCursor)
	if err != nil {
		return err
	}

	if result.NextCursor != nil {
		if cacheErr := savePurgeDanglingSigningKeyshareSecretsCursor(mc, cursorKey, *result.NextCursor); cacheErr != nil {
			sugar.Warnf("purge_dangling_signing_keyshare_secrets: failed to persist cursor (will resume from previous cursor or start over on next run): %v", cacheErr)
		}
	} else {
		if cacheErr := deletePurgeDanglingSigningKeyshareSecretsCursor(mc, cursorKey); cacheErr != nil {
			sugar.Warnf("purge_dangling_signing_keyshare_secrets: failed to clear cursor at end of pass (next run may rescan from stale cursor): %v", cacheErr)
		}
	}

	switch {
	case result.FoundFullDeleteBatch && result.NextCursor != nil:
		sugar.Warnf(
			"Found a full batch of dangling signing keyshare secrets; deleted %d after scanning %d aged candidates; cursor advanced to id=%s; additional dangling signing keyshare secrets may remain",
			result.DeletedCount,
			result.CandidateCount,
			result.NextCursor,
		)
	case result.FoundFullDeleteBatch:
		sugar.Infof(
			"Purged a full batch of %d dangling signing keyshare secrets out of %d aged candidates; scan reached end of aged data, cursor reset",
			result.DeletedCount,
			result.CandidateCount,
		)
	case result.NextCursor != nil:
		// Budget exhausted partway through the table. This is the steady-state
		// signal during cursor traversal — quiet but observable so we can spot a
		// stuck cursor (same id appearing across consecutive runs).
		sugar.Infof(
			"Scanned %d aged candidates and deleted %d dangling signing keyshare secrets; budget exhausted, cursor advanced to id=%s",
			result.CandidateCount,
			result.DeletedCount,
			result.NextCursor,
		)
	case result.DeletedCount > 0:
		sugar.Infof(
			"Purged %d dangling signing keyshare secrets out of %d aged candidates; scan reached end of aged data, cursor reset",
			result.DeletedCount,
			result.CandidateCount,
		)
	case result.CandidateCount > 0:
		sugar.Infof(
			"No dangling signing keyshare secrets found; %d aged candidates were all actively referenced; scan reached end of aged data, cursor reset",
			result.CandidateCount,
		)
	}
	return nil
}

func purgeDanglingSigningKeyshareSecretsBatch(
	ctx context.Context,
	cutoffID uuid.UUID,
	batchSize int,
	maxScanCount int,
	startCursor *uuid.UUID,
) (purgeDanglingSigningKeyshareSecretsBatchResult, error) {
	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to get main db from context: %w", err)
	}

	ephemeralDB, err := entephemeral.GetDbFromContext(ctx)
	if err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			return purgeDanglingSigningKeyshareSecretsBatchResult{}, nil
		}
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to get or create current ephemeral db for request: %w", err)
	}

	secretIDsToDelete, collectionResult, err := collectDanglingSigningKeyshareSecretIDs(
		ctx,
		mainDB,
		ephemeralDB,
		cutoffID,
		batchSize,
		maxScanCount,
		startCursor,
	)
	if err != nil {
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, err
	}

	result := purgeDanglingSigningKeyshareSecretsBatchResult{
		CandidateCount:       collectionResult.CandidateCount,
		FoundFullDeleteBatch: collectionResult.FoundFullDeleteBatch,
		NextCursor:           collectionResult.NextCursor,
	}
	if len(secretIDsToDelete) == 0 {
		return result, nil
	}

	deletedCount, err := ephemeralDB.SigningKeyshareSecret.Delete().
		Where(signingkeysharesecret.IDIn(secretIDsToDelete...)).
		Exec(ctx)
	if err != nil {
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to delete dangling signing keyshare secrets: %w", err)
	}
	result.DeletedCount = deletedCount

	return result, nil
}

func collectDanglingSigningKeyshareSecretIDs(
	ctx context.Context,
	mainDB *ent.Client,
	ephemeralDB *entephemeral.Client,
	cutoffID uuid.UUID,
	batchSize int,
	maxScanCount int,
	startCursor *uuid.UUID,
) ([]uuid.UUID, purgeDanglingSigningKeyshareSecretsCollectionResult, error) {
	secretIDsToDelete := make([]uuid.UUID, 0, batchSize)
	candidateCount := 0
	lastSeenID := startCursor
	reachedEndOfAgedData := false

	for len(secretIDsToDelete) < batchSize && candidateCount < maxScanCount {
		queryLimit := batchSize
		remainingScanCapacity := maxScanCount - candidateCount
		if remainingScanCapacity < queryLimit {
			queryLimit = remainingScanCapacity
		}

		query := ephemeralDB.SigningKeyshareSecret.Query().
			Where(signingkeysharesecret.IDLT(cutoffID)).
			Order(signingkeysharesecret.ByID(sql.OrderAsc())).
			Limit(queryLimit)
		if lastSeenID != nil {
			query = query.Where(signingkeysharesecret.IDGT(*lastSeenID))
		}

		candidates, err := query.
			Select(signingkeysharesecret.FieldID, signingkeysharesecret.FieldSigningKeyshareID, signingkeysharesecret.FieldVersion).
			All(ctx)
		if err != nil {
			return nil, purgeDanglingSigningKeyshareSecretsCollectionResult{}, fmt.Errorf("failed to query aged signing keyshare secrets: %w", err)
		}
		if len(candidates) == 0 {
			reachedEndOfAgedData = true
			break
		}

		candidateCount += len(candidates)
		batchDanglingIDs, err := getDanglingSigningKeyshareSecretIDs(ctx, mainDB, candidates)
		if err != nil {
			return nil, purgeDanglingSigningKeyshareSecretsCollectionResult{
				CandidateCount: candidateCount,
			}, err
		}

		remainingDeleteCapacity := batchSize - len(secretIDsToDelete)
		if len(batchDanglingIDs) > remainingDeleteCapacity {
			batchDanglingIDs = batchDanglingIDs[:remainingDeleteCapacity]
		}
		secretIDsToDelete = append(secretIDsToDelete, batchDanglingIDs...)

		lastCandidateID := candidates[len(candidates)-1].ID
		lastSeenID = &lastCandidateID
		if len(candidates) < queryLimit {
			reachedEndOfAgedData = true
			break
		}
	}

	result := purgeDanglingSigningKeyshareSecretsCollectionResult{
		CandidateCount:       candidateCount,
		FoundFullDeleteBatch: len(secretIDsToDelete) == batchSize,
	}
	if !reachedEndOfAgedData {
		result.NextCursor = lastSeenID
	}
	return secretIDsToDelete, result, nil
}

func getDanglingSigningKeyshareSecretIDs(
	ctx context.Context,
	mainDB *ent.Client,
	candidates []*entephemeral.SigningKeyshareSecret,
) ([]uuid.UUID, error) {
	signingKeyshareIDSet := make(map[uuid.UUID]struct{}, len(candidates))
	for _, secret := range candidates {
		signingKeyshareIDSet[secret.SigningKeyshareID] = struct{}{}
	}
	signingKeyshareIDs := make([]uuid.UUID, 0, len(signingKeyshareIDSet))
	for signingKeyshareID := range signingKeyshareIDSet {
		signingKeyshareIDs = append(signingKeyshareIDs, signingKeyshareID)
	}

	mainSigningKeyshares, err := mainDB.SigningKeyshare.Query().
		Where(signingkeyshare.IDIn(signingKeyshareIDs...)).
		Select(signingkeyshare.FieldID, signingkeyshare.FieldSecretVersion).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query signing keyshares by id: %w", err)
	}

	mainSigningKeysharesByID := make(map[uuid.UUID]*ent.SigningKeyshare, len(mainSigningKeyshares))
	for _, sk := range mainSigningKeyshares {
		mainSigningKeysharesByID[sk.ID] = sk
	}

	secretIDsToDelete := make([]uuid.UUID, 0, len(candidates))
	for _, candidate := range candidates {
		sk, ok := mainSigningKeysharesByID[candidate.SigningKeyshareID]
		if !ok || sk.SecretVersion == nil || *sk.SecretVersion != candidate.Version {
			secretIDsToDelete = append(secretIDsToDelete, candidate.ID)
		}
	}

	return secretIDsToDelete, nil
}

func purgeDanglingSigningKeyshareSecretsCursorKey(operatorIndex uint64) string {
	return fmt.Sprintf("%s:%d", purgeDanglingSigningKeyshareSecretsCursorKeyPrefix, operatorIndex)
}

func newPurgeDanglingSigningKeyshareSecretsMemcacheClient(cacheURI string) *memcache.Client {
	addr := strings.TrimPrefix(cacheURI, "memcaches://")
	addr = strings.TrimPrefix(addr, "memcache://")
	mc := memcache.New(addr)
	mc.Timeout = 2 * time.Second
	return mc
}

// loadPurgeDanglingSigningKeyshareSecretsCursor returns the persisted scan
// cursor, or nil if no usable cursor is available (no client, cache miss, or
// malformed value). nil means "start from the oldest aged row".
func loadPurgeDanglingSigningKeyshareSecretsCursor(mc *memcache.Client, key string) *uuid.UUID {
	if mc == nil {
		return nil
	}
	item, err := mc.Get(key)
	if err != nil {
		return nil
	}
	parsed, err := uuid.Parse(string(item.Value))
	if err != nil {
		return nil
	}
	return &parsed
}

func savePurgeDanglingSigningKeyshareSecretsCursor(mc *memcache.Client, key string, cursor uuid.UUID) error {
	if mc == nil {
		return nil
	}
	return mc.Set(&memcache.Item{
		Key:        key,
		Value:      []byte(cursor.String()),
		Expiration: purgeDanglingSigningKeyshareSecretsCursorTTL,
	})
}

func deletePurgeDanglingSigningKeyshareSecretsCursor(mc *memcache.Client, key string) error {
	if mc == nil {
		return nil
	}
	if err := mc.Delete(key); err != nil && !errors.Is(err, memcache.ErrCacheMiss) {
		return err
	}
	return nil
}
