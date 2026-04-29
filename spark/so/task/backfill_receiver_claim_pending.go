package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
)

const (
	// backfillReceiverClaimPendingBatchSize bounds rows touched per UPDATE so we
	// stay well under any per-statement lock-contention threshold while still
	// draining ~324K prod rows in a couple of minutes.
	backfillReceiverClaimPendingBatchSize = 5000
	// backfillReceiverClaimPendingSleep yields briefly between batches so the
	// task does not monopolise a connection during the active drain phase.
	backfillReceiverClaimPendingSleep = 100 * time.Millisecond
)

// backfillReceiverClaimPendingMu serialises ticks within a single SO process so
// a slow scheduler tick can't overlap the next one. Cross-SO concurrency is
// allowed — the UPDATE is idempotent (`WHERE status = 'INITIATED'` self-shrinks
// the candidate set), so two SOs running it concurrently produce duplicate work
// but no incorrectness. The window where both pods race is bounded to a single
// task tick after deploy; subsequent ticks find zero rows.
var backfillReceiverClaimPendingMu sync.Mutex

// backfillReceiverClaimPending flips pre-existing transfer_receivers rows that
// are still in INITIATED but whose parent transfer is already past sender key
// tweak from INITIATED → RECEIVER_CLAIM_PENDING. Once prod is drained the task
// is a fast no-op (a single zero-row UPDATE on every tick).
func backfillReceiverClaimPending(ctx context.Context) error {
	logger := logging.GetLoggerFromContext(ctx)
	sugar := logger.Sugar()

	if !backfillReceiverClaimPendingMu.TryLock() {
		sugar.Info("backfill_receiver_claim_pending: previous tick still running on this pod, skipping")
		return nil
	}
	defer backfillReceiverClaimPendingMu.Unlock()

	totalUpdated := 0
	batches := 0
	start := time.Now()

	for {
		if err := ctx.Err(); err != nil {
			sugar.Infof("backfill_receiver_claim_pending: context cancelled after %d batches (%d rows updated): %v", batches, totalUpdated, err)
			return err
		}

		updated, err := backfillReceiverClaimPendingBatch(ctx, backfillReceiverClaimPendingBatchSize)
		if err != nil {
			return fmt.Errorf("backfill batch %d failed (after %d rows updated): %w", batches+1, totalUpdated, err)
		}
		batches++
		totalUpdated += updated

		if updated > 0 {
			sugar.Infof("backfill_receiver_claim_pending: batch %d updated %d rows (total %d)", batches, updated, totalUpdated)
		}

		// A zero-row batch means no INITIATED candidates remain; we're done.
		// We can't terminate on `< batchSize` because the outer WHERE's status
		// re-check (see executeBackfillReceiverClaimPendingUpdate) lets a row
		// be silently skipped if it lost a race to a concurrent claim — that
		// produces a short batch even when more candidates exist.
		if updated == 0 {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backfillReceiverClaimPendingSleep):
		}
	}

	if totalUpdated > 0 {
		sugar.Infof("backfill_receiver_claim_pending: drained %d rows across %d batches in %s", totalUpdated, batches, time.Since(start))
	}
	return nil
}

// backfillReceiverClaimPendingBatch flips up to batchSize transfer_receivers
// rows whose parent transfer is past sender key tweak. Each batch runs in its
// own committed transaction so row locks are released between batches.
//
// Returns the number of rows actually updated. Callers must NOT use a return
// value < batchSize as a termination signal — a short batch can occur when the
// outer status re-check (see executeBackfillReceiverClaimPendingUpdate)
// silently skips a row that lost a race to a concurrent claim, even when more
// INITIATED candidates remain. Terminate only on a return value of 0.
func backfillReceiverClaimPendingBatch(ctx context.Context, batchSize int) (int, error) {
	client, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get db client: %w", err)
	}

	updated, err := executeBackfillReceiverClaimPendingUpdate(ctx, client, batchSize)
	if err != nil {
		return 0, err
	}

	// MarkTxDirty + DbCommit so we release row locks before sleeping and let
	// the next batch start a fresh tx (Session.GetClient lazily begins one).
	ent.MarkTxDirty(ctx)
	if commitErr := ent.DbCommit(ctx); commitErr != nil {
		return updated, fmt.Errorf("commit batch: %w", commitErr)
	}
	return updated, nil
}

// executeBackfillReceiverClaimPendingUpdate runs one bulk UPDATE that flips
// INITIATED → RECEIVER_CLAIM_PENDING for receivers whose parent transfer is
// already past sender key tweak.
//
// Plan: the inner SELECT is served by idx_transferreceiver_pending_pubkey_time
// (partial on INITIATED + receiver-side states; ~324K rows in prod, vs 62M in
// the full table), and the join filter narrows that to rows whose parent
// transfer is post-tweak. The IN-list of transfer statuses is fixed/small so
// the planner can keep this as a hash join without inflating the partial scan.
//
// The outer `AND status = 'INITIATED'` re-check is load-bearing, not
// redundant with the subselect: under READ COMMITTED, if a concurrent claim
// promotes a candidate row from INITIATED to RECEIVER_KEY_TWEAKED between
// the subquery snapshot and the UPDATE acquiring its row lock, Postgres
// re-evaluates the outer WHERE on the new row version. Without the status
// re-check, `id IN (cached_set)` is still true and we'd regress the row.
// With the re-check, the row is silently skipped.
func executeBackfillReceiverClaimPendingUpdate(ctx context.Context, client *ent.Client, batchSize int) (int, error) {
	//nolint:forbidigo // Raw SQL: bulk UPDATE with subselect across two tables; clearer than fighting Ent's join-in-update generator.
	res, err := client.ExecContext(ctx,
		`UPDATE transfer_receivers
		   SET status = 'RECEIVER_CLAIM_PENDING', update_time = NOW()
		 WHERE status = 'INITIATED'
		   AND id IN (
		   SELECT tr.id
		     FROM transfer_receivers tr
		     JOIN transfers t ON t.id = tr.transfer_id
		    WHERE tr.status = 'INITIATED'
		      AND t.status IN (
		        'SENDER_KEY_TWEAKED',
		        'RECEIVER_KEY_TWEAKED',
		        'RECEIVER_KEY_TWEAK_LOCKED',
		        'RECEIVER_KEY_TWEAK_APPLIED',
		        'RECEIVER_REFUND_SIGNED'
		      )
		    LIMIT $1
		 )`,
		batchSize,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
