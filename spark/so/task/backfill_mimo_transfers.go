package task

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/google/uuid"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/transfersender"
	"go.uber.org/zap"

	entsql "entgo.io/ent/dialect/sql"
)

const (
	backfillCursorKeyPrefix = "backfill_mimo_cursor_v2"
	// backfillWindowSize bounds each query to a fixed time range so no single
	// query scans an unbounded portion of the update_time index.
	backfillWindowSize = 6 * time.Hour
	// softDeadlineBuffer is subtracted from the task timeout to ensure we
	// return gracefully before the scheduler kills us.
	softDeadlineBuffer = 2 * time.Minute
)

var (
	backfillMu sync.Mutex
)

// backfillCursorState holds the keyset pagination cursor for the backfill.
type backfillCursorState struct {
	UpdateTime time.Time
	ID         uuid.UUID
}

func (c backfillCursorState) String() string {
	return fmt.Sprintf("%d:%s", c.UpdateTime.UnixMicro(), c.ID.String())
}

func parseBackfillCursor(raw string) (backfillCursorState, bool) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return backfillCursorState{}, false
	}
	var micros int64
	if _, err := fmt.Sscanf(parts[0], "%d", &micros); err != nil {
		return backfillCursorState{}, false
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return backfillCursorState{}, false
	}
	return backfillCursorState{
		UpdateTime: time.UnixMicro(micros),
		ID:         id,
	}, true
}

func backfillCursorKey(operatorIndex uint64) string {
	return fmt.Sprintf("%s:%d", backfillCursorKeyPrefix, operatorIndex)
}

func loadBackfillCursor(mc *memcache.Client, key string) (backfillCursorState, bool) {
	if mc == nil {
		return backfillCursorState{}, false
	}
	item, err := mc.Get(key)
	if err != nil {
		return backfillCursorState{}, false
	}
	return parseBackfillCursor(string(item.Value))
}

func saveBackfillCursor(mc *memcache.Client, key string, cursor backfillCursorState) {
	if mc == nil {
		return
	}
	_ = mc.Set(&memcache.Item{
		Key:        key,
		Value:      []byte(cursor.String()),
		Expiration: 14 * 24 * 3600, // 14 days
	})
}

// BackfillMimoResult holds the results of the backfill operation.
type BackfillMimoResult struct {
	TransfersProcessed int
	BackfillCursor     time.Time
	ReachedEnd         bool
}

// backfillMimoTransfers creates TransferSender, TransferReceiver, and TransferLeaf
// associations for historical Transfers that predate MIMO dual-writes.
//
// Two-phase design:
//   - Seek phase: jumps through 6-hour windows looking for transfers without senders.
//     This is read-only and can loop for up to (timeout - softDeadlineBuffer) to catch
//     up through months of empty history. The cursor is saved to memcached before exit
//     so seek progress is never lost.
//   - Write phase: processes up to batchSize transfers, then exits. The next invocation
//     picks up where we left off. This bounds DB write impact per invocation.
func backfillMimoTransfers(ctx context.Context, config *so.Config, db *ent.Client, batchSize int, timeout time.Duration) (BackfillMimoResult, error) {
	if !backfillMu.TryLock() {
		return BackfillMimoResult{}, nil
	}
	defer backfillMu.Unlock()

	logger := logging.GetLoggerFromContext(ctx).With(zap.String("task.name", "backfill_mimo_transfers"))

	// Initialize memcache client.
	var mc *memcache.Client
	if config.CacheURI != "" {
		mc = newMemcacheClient(config.CacheURI)
	}
	cursorKey := backfillCursorKey(config.Index)

	// Load cursor from memcached, or seed from the oldest transfer.
	cursor, hasCursor := loadBackfillCursor(mc, cursorKey)
	if !hasCursor {
		oldest, err := db.Transfer.Query().
			Where(enttransfer.NetworkNEQ(btcnetwork.Unspecified)).
			Order(enttransfer.ByUpdateTime(entsql.OrderAsc())).
			Limit(1).
			First(ctx)
		if err != nil {
			// No transfers at all — nothing to backfill.
			return BackfillMimoResult{ReachedEnd: true}, nil
		}
		cursor = backfillCursorState{
			UpdateTime: oldest.UpdateTime,
			ID:         uuid.UUID{}, // start before any ID at this timestamp
		}
		logger.Sugar().Infof("backfill seeded cursor from oldest transfer: %s",
			cursor.UpdateTime.Format(time.RFC3339))
	} else {
		logger.Sugar().Infof("backfill resuming from cursor: update_time=%s, id=%s",
			cursor.UpdateTime.Format(time.RFC3339), cursor.ID.String())
	}

	softDeadline := time.Now().Add(timeout - softDeadlineBuffer)
	totalProcessed := 0
	reachedEnd := false

	// Seek phase: jump through windows until we find work or reach the end.
	// This is read-only (bounded anti-join queries) so it's safe to loop freely.
	for time.Now().Before(softDeadline) {
		if cursor.UpdateTime.After(time.Now()) {
			reachedEnd = true
			break
		}

		windowEnd := cursor.UpdateTime.Add(backfillWindowSize)

		transfers, err := db.Transfer.Query().
			Where(
				enttransfer.Not(enttransfer.HasTransferSenders()),
				enttransfer.NetworkNEQ(btcnetwork.Unspecified),
				enttransfer.Or(
					enttransfer.UpdateTimeGT(cursor.UpdateTime),
					enttransfer.And(
						enttransfer.UpdateTimeEQ(cursor.UpdateTime),
						enttransfer.IDGT(cursor.ID),
					),
				),
				enttransfer.UpdateTimeLT(windowEnd),
			).
			Order(
				enttransfer.ByUpdateTime(entsql.OrderAsc()),
				enttransfer.ByID(entsql.OrderAsc()),
			).
			Limit(batchSize).
			All(ctx)
		if err != nil {
			logger.Error("failed to query transfers for backfill", zap.Error(err))
			break
		}

		if len(transfers) == 0 {
			// No gaps in this window — jump forward (seek).
			cursor = backfillCursorState{
				UpdateTime: windowEnd,
				ID:         uuid.UUID{},
			}
			saveBackfillCursor(mc, cursorKey, cursor)
			continue
		}

		// Write phase: process this batch and exit.
		for _, t := range transfers {
			if err := backfillSingleTransfer(ctx, db, t, logger); err != nil {
				logger.Warn("failed to backfill transfer, skipping",
					zap.String("transfer_id", t.ID.String()),
					zap.Error(err))
				continue
			}
			totalProcessed++
		}

		// Advance cursor past processed batch.
		last := transfers[len(transfers)-1]
		cursor = backfillCursorState{
			UpdateTime: last.UpdateTime,
			ID:         last.ID,
		}

		logger.Sugar().Infof("backfill batch: processed %d/%d transfers, cursor at %s",
			totalProcessed, len(transfers), cursor.UpdateTime.Format(time.RFC3339))

		// Exit after one write batch — next invocation continues from here.
		break
	}

	// Save cursor to memcached before returning.
	saveBackfillCursor(mc, cursorKey, cursor)

	if reachedEnd {
		logger.Sugar().Infof("backfill reached end of transfers, total processed this run: %d", totalProcessed)
	} else if totalProcessed > 0 {
		logger.Sugar().Infof("backfill wrote %d transfers, pausing at cursor %s",
			totalProcessed, cursor.UpdateTime.Format(time.RFC3339))
	}

	return BackfillMimoResult{
		TransfersProcessed: totalProcessed,
		BackfillCursor:     cursor.UpdateTime,
		ReachedEnd:         reachedEnd,
	}, nil
}

// backfillSingleTransfer creates TransferSender, TransferReceiver, and updates
// TransferLeaf associations for a single transfer within a transaction so a crash
// between steps can't leave permanently half-backfilled state.
func backfillSingleTransfer(ctx context.Context, db *ent.Client, t *ent.Transfer, logger *zap.Logger) error {
	// Check if sender already exists (race condition guard).
	exists, err := db.TransferSender.Query().
		Where(transfersender.TransferIDEQ(t.ID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("check existing sender: %w", err)
	}
	if exists {
		return nil
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	sender, err := tx.TransferSender.Create().
		SetTransferID(t.ID).
		SetIdentityPubkey(t.SenderIdentityPubkey).
		SetCreateTime(t.CreateTime).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create sender: %w", err)
	}

	receiver, err := tx.TransferReceiver.Create().
		SetTransferID(t.ID).
		SetIdentityPubkey(t.ReceiverIdentityPubkey).
		SetStatus(mapTransferToReceiverStatus(t.Status)).
		SetCreateTime(t.CreateTime).
		SetNillableCompletionTime(t.CompletionTime).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create receiver: %w", err)
	}

	err = tx.TransferLeaf.Update().
		Where(
			transferleaf.HasTransferWith(enttransfer.IDEQ(t.ID)),
			transferleaf.TransferSenderIDIsNil(),
		).
		SetTransferSenderID(sender.ID).
		SetTransferReceiverID(receiver.ID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update leafs: %w", err)
	}

	return tx.Commit()
}
