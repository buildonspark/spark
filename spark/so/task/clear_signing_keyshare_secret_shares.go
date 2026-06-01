package task

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/knobs"
)

const (
	clearSigningKeyshareSecretSharesInterval   = time.Minute
	clearSigningKeyshareSecretSharesTimeout    = 2 * time.Minute
	clearSigningKeyshareSecretSharesRunBudget  = 50 * time.Second
	clearSigningKeyshareSecretSharesBatchDelay = 250 * time.Millisecond
)

// var (not const) so tests can shrink it without creating thousands of fixtures.
var clearSigningKeyshareSecretSharesBatchSize = 1000

var clearSigningKeyshareSecretSharesTaskTimeout = clearSigningKeyshareSecretSharesTimeout

type clearSigningKeyshareSecretSharesBatchResult struct {
	SelectedCount int
	UpdatedCount  int
	// LastID is the ID of the last row examined in this batch. Callers use it as a
	// cursor for the next batch so a full batch of rows that race-cleared between
	// SELECT and UPDATE can't trap the loop re-selecting the same range.
	LastID *uuid.UUID
}

// clearSigningKeyshareSecretShares clears `signing_keyshares.secret_share` on rows
// whose `secret_version` is already populated, i.e. the canonical secret lives in the
// ephemeral DB and the main-DB column is now redundant.
//
// This is the post-backfill / post-dual-write-disable cleanup that lets us turn on
// main-DB backups without leaking plaintext secret bytes into snapshots. It is
// knob-gated (default off) so it stays dormant in operators that have not yet
// completed their own backfill + dual-write-disable rollout. Once every operator has
// completed the clear, the `SecretShare` Ent field and column can be removed in a
// coordinated schema migration.
//
// Safety invariant: only rows with `secret_share IS NOT NULL AND secret_version IS NOT NULL`
// are selected. The non-NULL `secret_version` guarantees an ephemeral row exists for
// the read fallback path in `SigningKeyshare.GetSecretShare(ctx)`; the cron is a no-op
// for rows missing either column.
func clearSigningKeyshareSecretShares(ctx context.Context, knobsService knobs.Knobs) error {
	if knobsService == nil || !knobsService.RolloutRandom(knobs.KnobSoClearSigningKeyshareSecretSharesEnabled, 0) {
		return nil
	}

	logger := logging.GetLoggerFromContext(ctx)
	start := time.Now()
	totalCleared := 0
	var cursor *uuid.UUID
	for {
		result, err := clearSigningKeyshareSecretSharesBatch(ctx, clearSigningKeyshareSecretSharesBatchSize, cursor)
		if err != nil {
			return err
		}

		if err := ent.DbCommit(ctx); err != nil {
			return fmt.Errorf("failed to commit signing keyshare secret_share clear batch: %w", err)
		}
		totalCleared += result.UpdatedCount
		if result.UpdatedCount > 0 {
			logger.Sugar().Infof("Cleared secret_share on %d signing keyshares (total: %d)", result.UpdatedCount, totalCleared)
		}
		if result.LastID != nil {
			cursor = result.LastID
		}
		if result.SelectedCount < clearSigningKeyshareSecretSharesBatchSize {
			break
		}
		if time.Since(start) >= clearSigningKeyshareSecretSharesRunBudget {
			logger.Sugar().Infof("Signing keyshare secret_share clear yielding after %d rows", totalCleared)
			break
		}
		if err := sleepWithContext(ctx, clearSigningKeyshareSecretSharesBatchDelay); err != nil {
			return err
		}
	}

	if totalCleared > 0 {
		logger.Sugar().Infof("Signing keyshare secret_share clear run processed %d keyshares", totalCleared)
	}
	return nil
}

func clearSigningKeyshareSecretSharesBatch(ctx context.Context, batchSize int, after *uuid.UUID) (clearSigningKeyshareSecretSharesBatchResult, error) {
	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return clearSigningKeyshareSecretSharesBatchResult{}, fmt.Errorf("failed to get main db from context: %w", err)
	}

	query := mainDB.SigningKeyshare.Query().
		Where(
			signingkeyshare.SecretShareNotNil(),
			signingkeyshare.SecretVersionNotNil(),
		)
	if after != nil {
		query = query.Where(signingkeyshare.IDGT(*after))
	}
	keyshares, err := query.
		Order(signingkeyshare.ByID(sql.OrderAsc())).
		Limit(batchSize).
		ForUpdate(sql.WithLockAction(sql.SkipLocked)).
		All(ctx)
	if err != nil {
		return clearSigningKeyshareSecretSharesBatchResult{}, fmt.Errorf("failed to query signing keyshares to clear secret_share: %w", err)
	}

	result := clearSigningKeyshareSecretSharesBatchResult{SelectedCount: len(keyshares)}
	if len(keyshares) == 0 {
		return result, nil
	}

	ids := make([]uuid.UUID, 0, len(keyshares))
	for _, keyshare := range keyshares {
		ids = append(ids, keyshare.ID)
	}

	// The re-check on secret_version protects against the theoretical case where a
	// concurrent writer cleared secret_version between our SELECT and UPDATE. Under
	// the row locks held by ForUpdate(SkipLocked) this should be impossible, but the
	// predicate is cheap and keeps the safety invariant explicit at every write site.
	updated, err := mainDB.SigningKeyshare.Update().
		Where(
			signingkeyshare.IDIn(ids...),
			signingkeyshare.SecretShareNotNil(),
			signingkeyshare.SecretVersionNotNil(),
		).
		ClearSecretShare().
		Save(ctx)
	if err != nil {
		return clearSigningKeyshareSecretSharesBatchResult{}, fmt.Errorf("failed to clear secret_share on batch of %d signing keyshares: %w", len(ids), err)
	}
	result.UpdatedCount = updated

	last := keyshares[len(keyshares)-1].ID
	result.LastID = &last
	return result, nil
}
