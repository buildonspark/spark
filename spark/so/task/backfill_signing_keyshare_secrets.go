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
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/knobs"
)

const (
	backfillSigningKeyshareSecretVersion     = int32(0)
	backfillSigningKeyshareSecretsInterval   = time.Minute
	backfillSigningKeyshareSecretsTimeout    = 2 * time.Minute
	backfillSigningKeyshareSecretsRunBudget  = 50 * time.Second
	backfillSigningKeyshareSecretsBatchDelay = 250 * time.Millisecond
)

// var (not const) so tests can shrink it without creating thousands of fixtures.
var backfillSigningKeyshareSecretsBatchSize = 1000

var backfillSigningKeyshareSecretsTaskTimeout = backfillSigningKeyshareSecretsTimeout

type backfillSigningKeyshareSecretsBatchResult struct {
	SelectedCount int
	UpdatedCount  int
	// LastID is the ID of the last row examined in this batch (regardless of whether
	// the row was successfully backfilled or skipped). Callers use it as a cursor for
	// the next batch so a full batch of persistently failing rows can't trap the loop
	// re-selecting the same rows every iteration.
	LastID *uuid.UUID
}

func backfillSigningKeyshareSecrets(ctx context.Context, knobsService knobs.Knobs) error {
	if knobsService == nil || !knobsService.RolloutRandom(knobs.KnobSoBackfillSigningKeyshareSecretsEnabled, 0) {
		return nil
	}
	if _, err := entephemeral.GetDbFromContext(ctx); err != nil {
		return fmt.Errorf("ephemeral db is required to backfill signing keyshare secrets: %w", err)
	}

	logger := logging.GetLoggerFromContext(ctx)
	start := time.Now()
	totalBackfilled := 0
	var cursor *uuid.UUID
	for {
		result, err := backfillSigningKeyshareSecretsBatch(ctx, backfillSigningKeyshareSecretsBatchSize, cursor)
		if err != nil {
			return err
		}

		if err := entephemeral.DbCommit(ctx); err != nil {
			return fmt.Errorf("failed to commit ephemeral signing keyshare secret backfill batch: %w", err)
		}
		if err := ent.DbCommit(ctx); err != nil {
			return fmt.Errorf("failed to commit signing keyshare secret_version backfill batch: %w", err)
		}
		totalBackfilled += result.UpdatedCount
		if result.UpdatedCount > 0 {
			logger.Sugar().Infof("Backfilled %d signing keyshare secrets (total: %d)", result.UpdatedCount, totalBackfilled)
		}
		if result.LastID != nil {
			cursor = result.LastID
		}
		if result.SelectedCount < backfillSigningKeyshareSecretsBatchSize {
			break
		}
		if time.Since(start) >= backfillSigningKeyshareSecretsRunBudget {
			logger.Sugar().Infof("Signing keyshare secret backfill yielding after %d rows", totalBackfilled)
			break
		}
		if err := sleepWithContext(ctx, backfillSigningKeyshareSecretsBatchDelay); err != nil {
			return err
		}
	}

	if totalBackfilled > 0 {
		logger.Sugar().Infof("Signing keyshare secret backfill run processed %d keyshares", totalBackfilled)
	}
	return nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func backfillSigningKeyshareSecretsBatch(ctx context.Context, batchSize int, after *uuid.UUID) (backfillSigningKeyshareSecretsBatchResult, error) {
	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return backfillSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to get main db from context: %w", err)
	}

	query := mainDB.SigningKeyshare.Query().
		Where(
			signingkeyshare.SecretVersionIsNil(),
			signingkeyshare.SecretShareNotNil(),
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
		return backfillSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to query signing keyshares requiring secret backfill: %w", err)
	}

	result := backfillSigningKeyshareSecretsBatchResult{SelectedCount: len(keyshares)}
	for _, keyshare := range keyshares {
		updated, err := backfillSigningKeyshareSecret(ctx, keyshare)
		if err != nil {
			// Skip rather than abort so a single bad row (e.g. a mismatched ephemeral
			// secret from manual tampering or a prior partial run) can't head-of-line
			// block every higher-ID keyshare. The cursor advances past skipped rows
			// so the next iteration in this run doesn't re-select them — they'll be
			// re-tried on the next scheduled run, which starts with cursor=nil.
			logging.GetLoggerFromContext(ctx).Sugar().Errorf(
				"Backfill skipping signing keyshare %s due to error: %v",
				keyshare.ID, err,
			)
			continue
		}
		result.UpdatedCount += updated
	}
	if len(keyshares) > 0 {
		last := keyshares[len(keyshares)-1].ID
		result.LastID = &last
	}
	return result, nil
}

func backfillSigningKeyshareSecret(ctx context.Context, keyshare *ent.SigningKeyshare) (int, error) {
	if keyshare.SecretShare == nil {
		return 0, fmt.Errorf("signing keyshare %s has null secret_version and null secret_share", keyshare.ID)
	}

	if err := ensureBackfillSigningKeyshareSecretVersion(ctx, keyshare); err != nil {
		return 0, err
	}

	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get main db from context: %w", err)
	}
	updated, err := mainDB.SigningKeyshare.Update().
		Where(
			signingkeyshare.IDEQ(keyshare.ID),
			signingkeyshare.SecretVersionIsNil(),
		).
		SetSecretVersion(backfillSigningKeyshareSecretVersion).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to set secret_version for signing keyshare %s: %w", keyshare.ID, err)
	}
	if updated == 0 {
		logging.GetLoggerFromContext(ctx).Sugar().Warnf(
			"Backfill did not update signing keyshare %s because secret_version was no longer null",
			keyshare.ID,
		)
	}
	return updated, nil
}

func ensureBackfillSigningKeyshareSecretVersion(ctx context.Context, keyshare *ent.SigningKeyshare) error {
	secret, err := entephemeral.GetOrCreateSigningKeyshareSecretVersion(
		ctx,
		keyshare.ID,
		backfillSigningKeyshareSecretVersion,
		*keyshare.SecretShare,
	)
	if err != nil {
		return fmt.Errorf("failed to ensure ephemeral secret for signing keyshare %s: %w", keyshare.ID, err)
	}
	if !secret.SecretShare.Equals(*keyshare.SecretShare) {
		return fmt.Errorf("signing keyshare %s already has mismatched ephemeral secret version %d", keyshare.ID, backfillSigningKeyshareSecretVersion)
	}
	return nil
}
