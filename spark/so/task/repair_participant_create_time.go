package task

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/transfer"
)

// Bump the version suffix to invalidate stale cursors and force a restart from the seed.
const repairCursorKeyPrefix = "repair_participant_create_time_cursor_v2"

// maxIDSentinel is the upper-bound tiebreaker UUID used when seeding descending
// keyset pagination; every real UUID sorts before it.
const maxIDSentinel = "ffffffff-ffff-ffff-ffff-ffffffffffff"

// Repair phases. Phase 1 walks cutoff → oldest (original behavior). When that
// completes, phase 2 walks now → cutoff to catch any divergence that may have
// leaked in after the original cutoff was picked. Phase 3 is the terminal state.
const (
	phaseBackfill = 1
	phaseForward  = 2
	phaseDone     = 3
)

// repairMu guards against in-pod scheduler overlap. Cross-pod overlap during
// rolling deploys is tolerated — UPDATEs are idempotent and cursor writes are
// last-write-wins; the worst case is duplicated work on a brief flap.
var repairMu sync.Mutex

type repairCursor struct {
	// UnixMicros is the transfers.create_time boundary for keyset pagination (descending),
	// stored with microsecond precision to avoid skipping transfers within the same second.
	UnixMicros int64
	// ID is the tiebreaker UUID string for rows sharing the same create_time.
	ID string
	// Phase tracks which repair pass this cursor belongs to.
	Phase int
}

func (c repairCursor) String() string {
	// Phase 1 uses the legacy 2-field format so an old task still running
	// during canary rollout continues to parse cursors. Once we transition to
	// phase > 1, an old task will error on the trailing ":N" — acceptable
	// because the new task handles everything and writes are idempotent.
	if c.Phase <= phaseBackfill {
		return fmt.Sprintf("%d:%s", c.UnixMicros, c.ID)
	}
	return fmt.Sprintf("%d:%s:%d", c.UnixMicros, c.ID, c.Phase)
}

func parseRepairCursor(raw string) (repairCursor, bool) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return repairCursor{}, false
	}
	v, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return repairCursor{}, false
	}
	// Distinguish legacy second-precision cursors from microsecond-precision ones.
	// Unix micros for any date after 2000-01-01 exceed 9.46e14, while unix seconds
	// for dates before 2100 are below 4.1e9. Add 1 second when converting so we
	// re-process the boundary rather than risk skipping transfers.
	if v < 1e13 {
		v = (v + 1) * 1e6
	}
	id := parts[1]
	phase := phaseBackfill
	// A numeric suffix (":N") in phaseBackfill..phaseDone range encodes the
	// phase; anything else is treated as a legacy 2-field cursor. Real UUIDs
	// never contain colons, so stripping a valid suffix can't corrupt an ID.
	if lastColon := strings.LastIndex(id, ":"); lastColon != -1 {
		if p, err := strconv.Atoi(id[lastColon+1:]); err == nil && p >= phaseBackfill && p <= phaseDone {
			phase = p
			id = id[:lastColon]
		}
	}
	return repairCursor{UnixMicros: v, ID: id, Phase: phase}, true
}

func repairCursorKey(operatorIndex uint64) string {
	return fmt.Sprintf("%s:%d", repairCursorKeyPrefix, operatorIndex)
}

func newMemcacheClient(cacheURI string) *memcache.Client {
	addr := strings.TrimPrefix(cacheURI, "memcaches://")
	addr = strings.TrimPrefix(addr, "memcache://")
	mc := memcache.New(addr)
	mc.Timeout = 2 * time.Second
	return mc
}

func loadCursor(mc *memcache.Client, key string) (repairCursor, bool) {
	item, err := mc.Get(key)
	if err != nil {
		return repairCursor{}, false
	}
	return parseRepairCursor(string(item.Value))
}

func saveCursor(mc *memcache.Client, key string, cursor repairCursor) {
	_ = mc.Set(&memcache.Item{
		Key:        key,
		Value:      []byte(cursor.String()),
		Expiration: 7 * 24 * 3600, // 7 days
	})
}

// repairCutoff is the point after which transfer_senders/transfer_receivers
// have correct create_time values. We only need to repair records before this date.
// Last divergent transfer: 2026-03-11 21:48:53 UTC (same transfer for both tables).
// +1 second so the cursor's < comparison includes that transfer.
var repairCutoff = time.Date(2026, time.March, 11, 21, 48, 54, 0, time.UTC)

// seedCursor returns a phase-1 cursor positioned at the repair cutoff, so the
// first paginated batch starts from the newest transfer that could need repair.
func seedCursor() repairCursor {
	return repairCursor{
		UnixMicros: repairCutoff.UnixMicro(),
		ID:         maxIDSentinel,
		Phase:      phaseBackfill,
	}
}

// seedPhaseForwardCursor returns a phase-2 cursor positioned at now, so the
// first batch starts from the newest transfer and walks back toward
// repairCutoff.
func seedPhaseForwardCursor(now time.Time) repairCursor {
	return repairCursor{
		UnixMicros: now.UnixMicro(),
		ID:         maxIDSentinel,
		Phase:      phaseForward,
	}
}

// transferRow carries just the two columns the repair needs — selecting only
// id/create_time also avoids scanning columns with malformed legacy data.
type transferRow struct {
	ID         uuid.UUID `json:"id"`
	CreateTime time.Time `json:"create_time"`
}

// repairParticipantTables lists the edge tables updated in lockstep. Order is
// stable so bulk and fallback paths update in the same sequence.
var repairParticipantTables = []string{"transfer_senders", "transfer_receivers"}

// isTransientPGError matches retryable serialization/deadlock failures. For
// these, the bulk UPDATE should be retried (by the scheduler) rather than
// degrading to the per-row fallback, which would also fail and just waste work.
func isTransientPGError(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	// 40001 = serialization_failure, 40P01 = deadlock_detected
	return pqErr.Code == "40001" || pqErr.Code == "40P01"
}

// validateParticipantTable guards the raw-SQL helpers below: `table` is
// interpolated directly into the UPDATE statement, so callers must only pass
// values from repairParticipantTables. Today all call sites loop over that
// slice, so this check is belt-and-suspenders against a future caller
// accidentally piping untrusted input through.
func validateParticipantTable(table string) error {
	if table != "transfer_senders" && table != "transfer_receivers" {
		return fmt.Errorf("unexpected participant table %q", table)
	}
	return nil
}

// bulkUpdateParticipantsTable issues a single UPDATE that uses UNNEST to
// apply per-row create_times to one edge table. The IS DISTINCT FROM guard
// skips rows whose create_time already matches, which both avoids pointless
// update_time bumps and keeps the RowsAffected count honest.
func bulkUpdateParticipantsTable(ctx context.Context, client *ent.Client, table string, transfers []transferRow) (int, error) {
	if err := validateParticipantTable(table); err != nil {
		return 0, err
	}
	ids := make([]uuid.UUID, len(transfers))
	cts := make([]time.Time, len(transfers))
	for i, t := range transfers {
		ids[i] = t.ID
		cts[i] = t.CreateTime
	}
	//nolint:forbidigo // Raw SQL required: create_time is Immutable in Ent schema and cannot be updated via generated builders.
	res, err := client.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s
			SET create_time = v.ct, update_time = NOW()
			FROM UNNEST($1::uuid[], $2::timestamptz[]) AS v(tid, ct)
			WHERE %s.transfer_id = v.tid
			  AND %s.create_time IS DISTINCT FROM v.ct`, table, table, table),
		pq.Array(ids), pq.Array(cts),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// updateSingleParticipant is the per-row fallback used when the bulk UPDATE
// hits a persistent (non-transient) error. Returns rows updated for this
// single (table, transfer) pair.
func updateSingleParticipant(ctx context.Context, client *ent.Client, table string, t transferRow) (int, error) {
	if err := validateParticipantTable(table); err != nil {
		return 0, err
	}
	//nolint:forbidigo // Raw SQL required: create_time is Immutable in Ent schema and cannot be updated via generated builders.
	res, err := client.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET create_time = $1, update_time = NOW()
			WHERE transfer_id = $2 AND create_time IS DISTINCT FROM $1`, table),
		t.CreateTime, t.ID,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// repairParticipantCreateTime walks transfers and sets
// transfer_senders.create_time and transfer_receivers.create_time to match
// transfers.create_time. Runs in two phases: first cutoff → oldest, then
// now → cutoff, so both historical drift and any post-cutoff divergence are
// covered. Uses a memcached cursor to track progress across restarts and an
// in-process mutex so a slow tick can't overlap the next scheduler fire.
func repairParticipantCreateTime(ctx context.Context, config *so.Config, client *ent.Client, batchSize int) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)
	sugar := logger.Sugar()

	if !repairMu.TryLock() {
		logger.Info("repair already in progress on this pod, skipping tick")
		return 0, nil
	}
	defer repairMu.Unlock()

	var mc *memcache.Client
	if config.CacheURI != "" {
		mc = newMemcacheClient(config.CacheURI)
	}

	cursorKey := repairCursorKey(config.Index)

	var cursor repairCursor
	var hasCursor bool
	if mc != nil {
		cursor, hasCursor = loadCursor(mc, cursorKey)
	}
	if !hasCursor {
		cursor = seedCursor()
		sugar.Infof("seeded repair cursor at cutoff: %s", cursor)
	} else {
		sugar.Infof("loaded repair cursor: %s", cursor)
	}

	// Terminal phase: both passes are done, nothing more to do.
	if cursor.Phase == phaseDone {
		return 0, nil
	}

	cursorTime := time.UnixMicro(cursor.UnixMicros)
	cursorID, err := uuid.Parse(cursor.ID)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor ID %q: %w", cursor.ID, err)
	}

	predicates := []predicate.Transfer{
		transfer.NetworkNEQ(btcnetwork.Unspecified),
		transfer.Or(
			transfer.CreateTimeLT(cursorTime),
			transfer.And(
				transfer.CreateTimeEQ(cursorTime),
				transfer.IDLT(cursorID),
			),
		),
	}
	// Phase 2 stops at repairCutoff so we don't redo territory phase 1 covers.
	if cursor.Phase == phaseForward {
		predicates = append(predicates, transfer.CreateTimeGTE(repairCutoff))
	}

	var transfers []transferRow
	err = client.Transfer.Query().
		Where(predicates...).
		Order(transfer.ByCreateTime(entsql.OrderDesc()), transfer.ByID(entsql.OrderDesc())).
		Limit(batchSize).
		Select(transfer.FieldID, transfer.FieldCreateTime).
		Scan(ctx, &transfers)
	if err != nil {
		return 0, fmt.Errorf("failed to query transfers: %w", err)
	}

	if len(transfers) == 0 {
		switch cursor.Phase {
		case phaseBackfill:
			next := seedPhaseForwardCursor(time.Now().UTC())
			sugar.Infof("phase 1 complete, transitioning to phase 2 at %s", next)
			if mc != nil {
				saveCursor(mc, cursorKey, next)
			}
		case phaseForward:
			done := repairCursor{Phase: phaseDone, ID: maxIDSentinel}
			logger.Info("phase 2 complete, repair finished")
			if mc != nil {
				saveCursor(mc, cursorKey, done)
			}
		}
		return 0, nil
	}

	// Mark the session dirty so the DatabaseMiddleware commits the transaction.
	// Raw ExecContext bypasses Ent's mutation layer and does not mark dirty automatically.
	ent.MarkTxDirty(ctx)

	totalRepaired := 0
	for _, table := range repairParticipantTables {
		n, err := bulkUpdateParticipantsTable(ctx, client, table, transfers)
		if err == nil {
			totalRepaired += n
			continue
		}
		if isTransientPGError(err) {
			// Surface transient failures so the scheduler retries the whole
			// batch rather than falling to per-row, which would hit the same
			// contention.
			return totalRepaired, fmt.Errorf("transient error on bulk update of %s: %w", table, err)
		}
		// Persistent bulk failure — isolate the poison pill by falling back to
		// per-row. Log the full batch size so dashboards can track fallback rate.
		sugar.Warnf("bulk update of %s failed (%d rows in batch), falling back to per-row: %v", table, len(transfers), err)
		for _, t := range transfers {
			rn, rerr := updateSingleParticipant(ctx, client, table, t)
			if rerr != nil {
				if isTransientPGError(rerr) {
					return totalRepaired, fmt.Errorf("transient error on fallback update of %s for transfer %s: %w", table, t.ID, rerr)
				}
				sugar.Errorf("skipping poisoned transfer %s on %s: %v", t.ID, table, rerr)
				continue
			}
			totalRepaired += rn
		}
	}

	// Advance cursor to the oldest transfer in this batch, preserving phase.
	oldest := transfers[len(transfers)-1]
	newCursor := repairCursor{
		UnixMicros: oldest.CreateTime.UnixMicro(),
		ID:         oldest.ID.String(),
		Phase:      cursor.Phase,
	}
	if mc != nil {
		saveCursor(mc, cursorKey, newCursor)
	}

	sugar.Infof("phase %d processed %d participant records across %d transfers, now at %s",
		cursor.Phase, totalRepaired, len(transfers), oldest.CreateTime.UTC().Format(time.RFC3339))
	return totalRepaired, nil
}
