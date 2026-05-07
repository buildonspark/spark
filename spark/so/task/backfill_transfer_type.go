package task

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
)

const (
	sp3050BackfillCursorKeyPrefix = "sp3050_backfill_transfer_type_cursor"
	// sp3050BackfillDoneSentinel marks the cursor entry once a sweep returns
	// zero rows. Subsequent ticks load this and no-op without touching the DB.
	// Delete the memcache entry to force a re-sweep.
	sp3050BackfillDoneSentinel     = "done"
	sp3050BackfillDefaultBatchSize = 2000
)

// sp3050BackfillCutoff bounds the sweep to transfers created before this
// instant. Rows created on/after the cutoff are populated at write time by
// the dual-write in createTransferSender / createTransferReceiver
// (Phase 1b), so the backfill doesn't need to touch them. Without a cutoff
// the natural "0 rows" done condition is unreachable while the table is
// live-writing — new transfers keep landing ahead of the cursor.
//
// Bump this constant if the dual-write deploy slips past this date.
var sp3050BackfillCutoff = time.Date(2026, time.May, 8, 0, 0, 0, 0, time.UTC)

// sp3050BackfillMu serialises ticks within a single SO process so a slow
// scheduler tick can't overlap the next one. Cross-pod overlap during
// rolling deploys is tolerated — the WHERE transfer_type IS NULL guard in
// the bulk UPDATE makes a duplicate write a no-op, and cursor writes are
// last-write-wins.
var sp3050BackfillMu sync.Mutex

// transferTypeRow carries just the columns the backfill needs. Selecting
// only id + type keeps the row decoder away from the network column on
// the three prod transfers where Ent's enum deserializer asserts on
// network=UNSPECIFIED. The NetworkNEQ predicate also filters them out
// at the SQL level, but selecting fewer columns is cheaper anyway.
type transferTypeRow struct {
	ID   uuid.UUID       `json:"id"`
	Type st.TransferType `json:"type"`
}

// backfillTransferType walks transfers and populates
// transfer_senders.transfer_type and transfer_receivers.transfer_type
// from the parent transfers.type. Single forward sweep ordered by
// transfers.id (PK btree), bounded above by sp3050BackfillCutoff so the
// "0 rows" done condition is reachable while the table is live-writing.
// Cursor is persisted in memcache; once a sweep returns zero rows, a
// "done" sentinel is written so subsequent ticks no-op without touching
// the DB.
//
// One batch per invocation. Throughput is set by the scheduler interval
// × batch size; comfortably fits inside the default task timeout.
func backfillTransferType(ctx context.Context, config *so.Config, batchSize int) error {
	logger := logging.GetLoggerFromContext(ctx)
	sugar := logger.Sugar()

	if !sp3050BackfillMu.TryLock() {
		sugar.Info("backfill_transfer_type: previous tick still running on this pod, skipping")
		return nil
	}
	defer sp3050BackfillMu.Unlock()

	if batchSize <= 0 {
		sugar.Warnf("backfill_transfer_type: invalid batch size %d, skipping", batchSize)
		return nil
	}

	var mc *memcache.Client
	if config.CacheURI != "" {
		mc = newSP3050MemcacheClient(config.CacheURI)
	}

	cursorKey := sp3050BackfillCursorKey(config.Index)
	cursor, done := loadSP3050Cursor(mc, cursorKey)
	if done {
		return nil
	}

	start := time.Now()

	rows, err := fetchSP3050Batch(ctx, cursor, batchSize)
	if err != nil {
		return fmt.Errorf("fetch batch (cursor=%s): %w", cursor, err)
	}

	if len(rows) == 0 {
		saveSP3050Done(mc, cursorKey)
		sugar.Infof("backfill_transfer_type: complete in %s — no rows below cutoff %s remain",
			time.Since(start), sp3050BackfillCutoff.Format(time.RFC3339))
		return nil
	}

	senders, receivers, err := updateSP3050Edges(ctx, rows)
	if err != nil {
		return fmt.Errorf("update batch (cursor=%s): %w", cursor, err)
	}

	// Commit so row locks release before this tick returns and the next
	// scheduled tick starts a fresh tx.
	if commitErr := ent.DbCommit(ctx); commitErr != nil {
		return fmt.Errorf("commit batch: %w", commitErr)
	}

	cursor = rows[len(rows)-1].ID
	saveSP3050Cursor(mc, cursorKey, cursor)

	sugar.Infof("backfill_transfer_type: processed %d transfers (%d sender + %d receiver rows updated) in %s, cursor at id=%s",
		len(rows), senders, receivers, time.Since(start), cursor)

	if len(rows) < batchSize {
		saveSP3050Done(mc, cursorKey)
		sugar.Infof("backfill_transfer_type: complete (short final batch) — no more rows below cutoff %s",
			sp3050BackfillCutoff.Format(time.RFC3339))
	}
	return nil
}

func sp3050BackfillCursorKey(operatorIndex uint64) string {
	return fmt.Sprintf("%s:%d", sp3050BackfillCursorKeyPrefix, operatorIndex)
}

func newSP3050MemcacheClient(cacheURI string) *memcache.Client {
	addr := strings.TrimPrefix(cacheURI, "memcaches://")
	addr = strings.TrimPrefix(addr, "memcache://")
	mc := memcache.New(addr)
	mc.Timeout = 2 * time.Second
	return mc
}

// loadSP3050Cursor reads the cursor from memcache. Returns:
//   - (uuid.Nil, true)  → "done" sentinel set; sweep complete
//   - (uuid, false)     → resume from id > uuid
//   - (uuid.Nil, false) → no entry; first run, seek from id > zero-uuid
func loadSP3050Cursor(mc *memcache.Client, key string) (uuid.UUID, bool) {
	if mc == nil {
		return uuid.Nil, false
	}
	item, err := mc.Get(key)
	if err != nil {
		return uuid.Nil, false
	}
	raw := string(item.Value)
	if raw == sp3050BackfillDoneSentinel {
		return uuid.Nil, true
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false
	}
	return parsed, false
}

func saveSP3050Cursor(mc *memcache.Client, key string, cursor uuid.UUID) {
	if mc == nil {
		return
	}
	_ = mc.Set(&memcache.Item{
		Key:        key,
		Value:      []byte(cursor.String()),
		Expiration: 7 * 24 * 3600,
	})
}

func saveSP3050Done(mc *memcache.Client, key string) {
	if mc == nil {
		return
	}
	_ = mc.Set(&memcache.Item{
		Key:        key,
		Value:      []byte(sp3050BackfillDoneSentinel),
		Expiration: 30 * 24 * 3600,
	})
}

func fetchSP3050Batch(ctx context.Context, cursor uuid.UUID, batchSize int) ([]transferTypeRow, error) {
	client, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("get db client: %w", err)
	}

	var rows []transferTypeRow
	err = client.Transfer.Query().
		Where(
			transfer.NetworkNEQ(btcnetwork.Unspecified),
			transfer.IDGT(cursor),
			transfer.CreateTimeLT(sp3050BackfillCutoff),
		).
		Order(transfer.ByID(entsql.OrderAsc())).
		Limit(batchSize).
		Select(transfer.FieldID, transfer.FieldType).
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("query transfers: %w", err)
	}
	return rows, nil
}

func updateSP3050Edges(ctx context.Context, rows []transferTypeRow) (int, int, error) {
	senders, err := bulkUpdateSP3050Edge(ctx, "transfer_senders", rows)
	if err != nil {
		return 0, 0, fmt.Errorf("update transfer_senders: %w", err)
	}
	receivers, err := bulkUpdateSP3050Edge(ctx, "transfer_receivers", rows)
	if err != nil {
		return senders, 0, fmt.Errorf("update transfer_receivers: %w", err)
	}
	return senders, receivers, nil
}

// bulkUpdateSP3050Edge applies per-row transfer_type values to one edge
// table via UNNEST. The IS NULL guard makes the UPDATE a no-op for rows
// already populated by the dual-write (Phase 1b), so the task is
// re-runnable without double-writes. The seek to find rows uses the
// (transfer_id, identity_pubkey) UNIQUE index.
func bulkUpdateSP3050Edge(ctx context.Context, table string, rows []transferTypeRow) (int, error) {
	if err := validateSP3050Table(table); err != nil {
		return 0, err
	}
	client, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("get db client: %w", err)
	}

	ids := make([]uuid.UUID, len(rows))
	types := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		types[i] = string(r.Type)
	}

	//nolint:forbidigo // Raw SQL: bulk UPDATE with UNNEST; clearer than fighting Ent's join-in-update generator.
	res, err := client.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s
			SET transfer_type = v.tt, update_time = NOW()
			FROM UNNEST($1::uuid[], $2::text[]) AS v(tid, tt)
			WHERE %s.transfer_id = v.tid
			  AND %s.transfer_type IS NULL`, table, table, table),
		pq.Array(ids), pq.Array(types),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// validateSP3050Table whitelists the table-name input to the raw UPDATE.
// All call sites pass string literals from a tight set, so this is
// belt-and-suspenders against future drift.
func validateSP3050Table(table string) error {
	if table != "transfer_senders" && table != "transfer_receivers" {
		return fmt.Errorf("unexpected sp3050 backfill table %q", table)
	}
	return nil
}
