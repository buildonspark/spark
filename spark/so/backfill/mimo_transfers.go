package backfill

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/transfersender"
	"go.uber.org/zap"
)

const initScanBatchSize = 10000

// hardcodedCursorFallback is used when memcached has no cursor.
// Both DKG pods have verified all transfers before this point have senders.
var hardcodedCursorFallback = time.Date(2025, time.November, 20, 18, 0, 0, 0, time.UTC)

var (
	backfillMu          sync.Mutex
	backfillCursor      = hardcodedCursorFallback
	lastSeenID          uuid.UUID // tiebreaker for keyset pagination in initBackfillCursor
	backfillInitialized bool
	mcClient            *memcache.Client // lazily initialized
)

// resetBackfillState resets the cursor state for testing.
func resetBackfillState() {
	backfillCursor = time.Time{}
	lastSeenID = uuid.UUID{}
	backfillInitialized = false
	mcClient = nil
}

func cursorCacheKey(operatorIndex uint64) string {
	return fmt.Sprintf("backfill_mimo_cursor:%d", operatorIndex)
}

// getMemcacheClient lazily initializes a memcache client from config.CacheURI.
func getMemcacheClient(config *so.Config) *memcache.Client {
	if mcClient != nil {
		return mcClient
	}
	uri := strings.TrimSpace(config.CacheURI)
	uri = strings.TrimPrefix(uri, "memcaches://")
	uri = strings.TrimPrefix(uri, "memcache://")
	if uri == "" {
		return nil
	}
	servers := strings.Split(uri, ",")
	mcClient = memcache.New(servers...)
	return mcClient
}

// loadCursorFromCache attempts to restore cursor state from memcached.
// Returns true if a cached cursor was found and applied.
func loadCursorFromCache(config *so.Config, logger *zap.Logger) bool {
	mc := getMemcacheClient(config)
	if mc == nil {
		return false
	}
	item, err := mc.Get(cursorCacheKey(config.Index))
	if err != nil {
		if !errors.Is(err, memcache.ErrCacheMiss) {
			logger.Sugar().Warnf("failed to load backfill cursor from cache: %v", err)
		}
		return false
	}
	// Format: "<unix_seconds>:<uuid>"
	raw := string(item.Value)
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		logger.Sugar().Warnf("malformed backfill cursor cache value: %q", raw)
		return false
	}
	unix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		logger.Sugar().Warnf("malformed backfill cursor cache timestamp: %v", err)
		return false
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		logger.Sugar().Warnf("malformed backfill cursor cache UUID: %v", err)
		return false
	}
	cached := time.Unix(unix, 0)
	if cached.After(backfillCursor) {
		backfillCursor = cached
		lastSeenID = id
		logger.Sugar().Infof("backfill cursor restored from cache: %s (unix: %d)", cached.Format(time.RFC3339), unix)
		return true
	}
	return false
}

// saveCursorToCache persists the current cursor state to memcached.
func saveCursorToCache(config *so.Config, logger *zap.Logger) {
	mc := getMemcacheClient(config)
	if mc == nil {
		return
	}
	val := fmt.Sprintf("%d:%s", backfillCursor.Unix(), lastSeenID.String())
	if err := mc.Set(&memcache.Item{
		Key:   cursorCacheKey(config.Index),
		Value: []byte(val),
		// No expiration — persist until memcached restarts or evicts.
	}); err != nil {
		logger.Sugar().Warnf("failed to save backfill cursor to cache: %v", err)
	}
}

// BackfillMimoResult holds the results of the backfill operation.
type BackfillMimoResult struct {
	TransfersCreated int
	BackfillCursor   time.Time
}

// BackfillMimoTransfers runs two backfill operations:
//  1. Creates TransferSender/TransferReceiver/TransferLeaf associations for
//     historical Transfers that predate MIMO writes.
//  2. Syncs stale TransferReceiver statuses for receivers created before
//     dual-write status updates were enabled.
func BackfillMimoTransfers(ctx context.Context, config *so.Config, batchSize int) (BackfillMimoResult, error) {
	if !backfillMu.TryLock() {
		return BackfillMimoResult{}, nil
	}
	defer backfillMu.Unlock()

	if !backfillInitialized && config != nil {
		loadCursorFromCache(config, logging.GetLoggerFromContext(ctx))
	}

	created, err := backfillCreateMimoRecords(ctx, batchSize)
	if err != nil {
		return BackfillMimoResult{}, fmt.Errorf("backfill create records: %w", err)
	}

	if config != nil {
		saveCursorToCache(config, logging.GetLoggerFromContext(ctx))
	}

	return BackfillMimoResult{
		TransfersCreated: created,
		BackfillCursor:   backfillCursor,
	}, nil
}

// MonitorReceiverStatusMismatches checks recently-updated Transfers for
// TransferReceivers whose status doesn't match the expected mapped status.
// This detects dual-write failures across ALL states, not just terminal ones.
//
// Uses REPEATABLE READ isolation so that the Transfer query and the eager-loaded
// TransferReceivers query see the same database snapshot, eliminating false
// positives from concurrent commits between the two reads.
//
// Accepts a raw (non-tx-backed) *ent.Client because the task middleware's
// DatabaseMiddleware wraps every task in a transaction, and BeginTx cannot
// nest inside an existing transaction. The caller constructs a fresh ent client
// from the raw *sql.DB provided by RequiresRawDBClient.
func MonitorReceiverStatusMismatches(ctx context.Context, db *ent.Client, batchSize int) (int, error) {
	logger := logging.GetLoggerFromContext(ctx).With(zap.String("task.name", "monitor_receiver_status_mismatches"))

	tx, err := db.BeginTx(ctx, &stdsql.TxOptions{
		ReadOnly:  true,
		Isolation: stdsql.LevelRepeatableRead,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to begin read-only tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	transfers, err := tx.Transfer.Query().
		Where(
			enttransfer.UpdateTimeGTE(now.Add(-30*time.Second)),
			enttransfer.HasTransferReceivers(),
		).
		WithTransferReceivers().
		Limit(batchSize).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to query recent transfers: %w", err)
	}

	mismatches := 0
	for _, t := range transfers {
		expectedStatus := MapTransferToReceiverStatus(t.Status)
		for _, r := range t.Edges.TransferReceivers {
			if r.Status != expectedStatus {
				mismatches++
				logger.With(
					zap.String("transfer_id", t.ID.String()),
				).Sugar().Warnf("receiver %s status mismatch: receiver=%s, expected=%s (transfer=%s)",
					r.ID, r.Status, expectedStatus, t.Status)
			}
		}
	}

	if mismatches > 0 {
		logger.Sugar().Warnf("found %d receiver status mismatches across %d recent transfers", mismatches, len(transfers))
	}

	return mismatches, nil
}

// initBackfillCursor scans forward through transfers ordered by update_time
// to find the first transfer without a TransferSender record, setting the
// cursor to its update_time. If all transfers have senders, cursor is set to
// now (nothing left to backfill).
//
// This isn't super efficient, but it's a one-time (per deployment) no-lock scan and
// works best for this use case to avoid timeouts with expensive full-table anti-join queries.
func initBackfillCursor(ctx context.Context, db *ent.Client, batchSize int) error {
	for {
		query := db.Transfer.Query().
			Where(
				enttransfer.NetworkNEQ(btcnetwork.Unspecified),
			).
			Order(enttransfer.ByUpdateTime(sql.OrderAsc()), enttransfer.ByID(sql.OrderAsc())).
			Limit(batchSize)

		// Keyset pagination: pick up either at a later timestamp, or at
		// the same timestamp with a higher ID. This avoids skipping
		// records when multiple transfers share the same update_time
		// (pure GT on timestamp would jump past all of them).
		if !backfillCursor.IsZero() {
			query = query.Where(
				enttransfer.Or(
					enttransfer.UpdateTimeGT(backfillCursor),
					enttransfer.And(
						enttransfer.UpdateTimeEQ(backfillCursor),
						enttransfer.IDGT(lastSeenID),
					),
				),
			)
		}

		transfers, err := query.All(ctx)
		if err != nil {
			return fmt.Errorf("init cursor scan: %w", err)
		}

		if len(transfers) == 0 {
			// All transfers have been scanned; everything is backfilled.
			backfillCursor = time.Now()
			backfillInitialized = true
			return nil
		}

		ids := make([]uuid.UUID, len(transfers))
		for i, t := range transfers {
			ids[i] = t.ID
		}

		var existingIDs []uuid.UUID
		err = db.TransferSender.Query().
			Where(transfersender.TransferIDIn(ids...)).
			Select(transfersender.FieldTransferID).
			Scan(ctx, &existingIDs)
		if err != nil {
			return fmt.Errorf("init cursor sender check: %w", err)
		}

		existingSet := make(map[uuid.UUID]bool, len(existingIDs))
		for _, id := range existingIDs {
			existingSet[id] = true
		}

		for _, t := range transfers {
			if !existingSet[t.ID] {
				backfillCursor = t.UpdateTime
				lastSeenID = t.ID
				backfillInitialized = true
				return nil
			}
		}

		if len(transfers) < batchSize {
			// Reached end of table with no gaps — backfill is complete.
			backfillCursor = time.Now()
			backfillInitialized = true
			return nil
		}

		// Entire batch had senders; advance past it.
		last := transfers[len(transfers)-1]
		backfillCursor = last.UpdateTime
		lastSeenID = last.ID
	}
}

// backfillCreateMimoRecords finds Transfers without TransferSender records and
// creates the corresponding TransferSender, TransferReceiver, and TransferLeaf
// associations. Uses a cursor to avoid scanning from the beginning of the table.
func backfillCreateMimoRecords(ctx context.Context, batchSize int) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get db from context: %w", err)
	}

	if !backfillInitialized {
		if err := initBackfillCursor(ctx, db, initScanBatchSize); err != nil {
			return 0, fmt.Errorf("failed to init backfill cursor: %w", err)
		}
	}

	// The anti-join is acceptable here because the cursor narrows the scan
	// window and the limit caps the result set.
	transfers, err := db.Transfer.Query().
		Where(
			enttransfer.UpdateTimeGTE(backfillCursor),
			enttransfer.Not(enttransfer.HasTransferSenders()),
			enttransfer.NetworkNEQ(btcnetwork.Unspecified),
		).
		Order(enttransfer.ByUpdateTime(sql.OrderAsc())).
		Limit(batchSize).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to query transfers without senders: %w", err)
	}

	if len(transfers) == 0 {
		return 0, nil
	}

	processed := 0
	for _, t := range transfers {
		sender, err := db.TransferSender.Create().
			SetTransferID(t.ID).
			SetIdentityPubkey(t.SenderIdentityPubkey).
			Save(ctx)
		if err != nil {
			logger.Warn(fmt.Sprintf("backfill_mimo_transfers: failed to create sender for transfer %s, skipping", t.ID), zap.Error(err))
			continue
		}

		receiverCreate := db.TransferReceiver.Create().
			SetTransferID(t.ID).
			SetIdentityPubkey(t.ReceiverIdentityPubkey).
			SetStatus(MapTransferToReceiverStatus(t.Status))
		if t.CompletionTime != nil {
			receiverCreate = receiverCreate.SetNillableCompletionTime(t.CompletionTime)
		}
		receiver, err := receiverCreate.Save(ctx)
		if err != nil {
			logger.Warn(fmt.Sprintf("backfill_mimo_transfers: failed to create receiver for transfer %s, skipping", t.ID), zap.Error(err))
			_ = db.TransferSender.DeleteOne(sender).Exec(ctx)
			continue
		}

		err = db.TransferLeaf.Update().
			Where(
				transferleaf.HasTransferWith(enttransfer.IDEQ(t.ID)),
				transferleaf.TransferSenderIDIsNil(),
			).
			SetTransferSenderID(sender.ID).
			SetTransferReceiverID(receiver.ID).
			Exec(ctx)
		if err != nil {
			logger.Warn(fmt.Sprintf("backfill_mimo_transfers: failed to update leaves for transfer %s, skipping", t.ID), zap.Error(err))
			_ = db.TransferReceiver.DeleteOne(receiver).Exec(ctx)
			_ = db.TransferSender.DeleteOne(sender).Exec(ctx)
			continue
		}

		processed++
	}

	// Advance cursor only when the entire batch succeeded; the anti-join
	// filters already-processed transfers on the next run regardless.
	if processed == len(transfers) {
		last := transfers[len(transfers)-1]
		backfillCursor = last.UpdateTime
		lastSeenID = last.ID
	}

	return processed, nil
}

// MapTransferToReceiverStatus maps a Transfer status to the corresponding TransferReceiver status.
func MapTransferToReceiverStatus(s st.TransferStatus) st.TransferReceiverStatus {
	switch s {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweaked:
		return st.TransferReceiverStatusSenderInitiated
	case st.TransferStatusReceiverKeyTweaked:
		return st.TransferReceiverStatusKeyTweaked
	case st.TransferStatusReceiverKeyTweakLocked:
		return st.TransferReceiverStatusKeyTweakLocked
	case st.TransferStatusReceiverKeyTweakApplied:
		return st.TransferReceiverStatusKeyTweakApplied
	case st.TransferStatusReceiverRefundSigned:
		return st.TransferReceiverStatusRefundSigned
	case st.TransferStatusCompleted:
		return st.TransferReceiverStatusCompleted
	case st.TransferStatusExpired, st.TransferStatusReturned:
		return st.TransferReceiverStatusCancelled
	default:
		return st.TransferReceiverStatusSenderInitiated
	}
}
