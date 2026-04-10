package task

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/google/uuid"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/transfer"
)

// Bump the version suffix to invalidate stale cursors and force a restart from the seed.
const repairCursorKeyPrefix = "repair_participant_create_time_cursor_v2"

// repairTargetWalletPubkey is the high-volume wallet whose participant records need
// targeted repair. This wallet has 177K+ sender and 190K+ receiver rows, many from a
// backfill in March that stamped incorrect create_times. The general repair task would
// take too long to reach this wallet's oldest transfers (September 2025).
var repairTargetWalletPubkey = keys.MustParsePublicKeyHex("02894808873b896e21d29856a6d7bb346fb13c019739adb9bf0b6a8b7e28da53da")

type repairCursor struct {
	// UnixMicros is the transfers.create_time boundary for keyset pagination (descending),
	// stored with microsecond precision to avoid skipping transfers within the same second.
	UnixMicros int64
	// ID is the tiebreaker UUID string for rows sharing the same create_time.
	ID string
}

func (c repairCursor) String() string {
	return fmt.Sprintf("%d:%s", c.UnixMicros, c.ID)
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
	return repairCursor{UnixMicros: v, ID: parts[1]}, true
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

// seedCursor returns a cursor positioned at the repair cutoff, so the first
// paginated batch starts from the newest transfer that could need repair.
func seedCursor() repairCursor {
	return repairCursor{
		UnixMicros: repairCutoff.UnixMicro(),
		ID:         "ffffffff-ffff-ffff-ffff-ffffffffffff",
	}
}

// repairWalletParticipantCreateTime repairs transfer_senders and transfer_receivers
// create_time for a single high-volume wallet. This runs independently of the general
// repair task so the wallet doesn't have to wait for the full backwards scan to reach
// its oldest transfers. Processes one batch per invocation; uses a memcached cursor
// keyed by pubkey to track progress.
func repairWalletParticipantCreateTime(ctx context.Context, config *so.Config, client *ent.Client, pubkey keys.Public, batchSize int) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)

	var mc *memcache.Client
	if config.CacheURI != "" {
		mc = newMemcacheClient(config.CacheURI)
	}

	cursorKey := fmt.Sprintf("repair_wallet_participant_ct:%x:%d", pubkey.Serialize(), config.Index)

	var cursor repairCursor
	var hasCursor bool
	if mc != nil {
		cursor, hasCursor = loadCursor(mc, cursorKey)
	}
	if !hasCursor {
		cursor = seedCursor()
		logger.Sugar().Infof("seeded wallet repair cursor at cutoff: %s", cursor)
	} else {
		logger.Sugar().Infof("loaded wallet repair cursor: %s", cursor)
	}

	cursorTime := time.UnixMicro(cursor.UnixMicros)
	cursorID, err := uuid.Parse(cursor.ID)
	if err != nil {
		return 0, fmt.Errorf("invalid wallet cursor ID %q: %w", cursor.ID, err)
	}

	type transferRow struct {
		ID         uuid.UUID `json:"id"`
		CreateTime time.Time `json:"create_time"`
	}
	var transfers []transferRow
	err = client.Transfer.Query().
		Where(
			transfer.NetworkNEQ(btcnetwork.Unspecified),
			transfer.Or(
				transfer.SenderIdentityPubkeyEQ(pubkey),
				transfer.ReceiverIdentityPubkeyEQ(pubkey),
			),
			transfer.Or(
				transfer.CreateTimeLT(cursorTime),
				transfer.And(
					transfer.CreateTimeEQ(cursorTime),
					transfer.IDLT(cursorID),
				),
			),
		).
		Order(transfer.ByCreateTime(entsql.OrderDesc()), transfer.ByID(entsql.OrderDesc())).
		Limit(batchSize).
		Select(transfer.FieldID, transfer.FieldCreateTime).
		Scan(ctx, &transfers)
	if err != nil {
		return 0, fmt.Errorf("failed to query wallet transfers: %w", err)
	}

	if len(transfers) == 0 {
		logger.Info("wallet repair complete, no more transfers to process")
		return 0, nil
	}

	ent.MarkTxDirty(ctx)

	totalRepaired := 0
	for _, t := range transfers {
		for _, table := range []string{"transfer_senders", "transfer_receivers"} {
			//nolint:forbidigo // Raw SQL required: create_time is Immutable in Ent schema.
			res, err := client.ExecContext(ctx,
				fmt.Sprintf(`UPDATE %s SET create_time = $1, update_time = NOW() WHERE transfer_id = $2`, table),
				t.CreateTime, t.ID,
			)
			if err != nil {
				return totalRepaired, fmt.Errorf("failed to update %s for transfer %s: %w", table, t.ID, err)
			}
			n, _ := res.RowsAffected()
			totalRepaired += int(n)
		}
	}

	oldest := transfers[len(transfers)-1]
	newCursor := repairCursor{UnixMicros: oldest.CreateTime.UnixMicro(), ID: oldest.ID.String()}
	if mc != nil {
		saveCursor(mc, cursorKey, newCursor)
	}

	logger.Sugar().Infof("wallet repair: processed %d participant records across %d transfers, now at %s",
		totalRepaired, len(transfers), oldest.CreateTime.UTC().Format(time.RFC3339))
	return totalRepaired, nil
}

// repairParticipantCreateTime walks transfers from newest to oldest and sets
// transfer_senders.create_time and transfer_receivers.create_time to match
// transfers.create_time. Uses a memcached cursor to track progress across restarts.
func repairParticipantCreateTime(ctx context.Context, config *so.Config, client *ent.Client, batchSize int) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)

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
		logger.Sugar().Infof("seeded repair cursor at cutoff: %s", cursor)
	} else {
		logger.Sugar().Infof("loaded repair cursor: %s", cursor)
	}

	cursorTime := time.UnixMicro(cursor.UnixMicros)
	cursorID, err := uuid.Parse(cursor.ID)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor ID %q: %w", cursor.ID, err)
	}

	// Only select id and create_time to avoid scanning columns with malformed data.
	type transferRow struct {
		ID         uuid.UUID `json:"id"`
		CreateTime time.Time `json:"create_time"`
	}
	var transfers []transferRow
	err = client.Transfer.Query().
		Where(
			transfer.NetworkNEQ(btcnetwork.Unspecified),
			transfer.Or(
				transfer.CreateTimeLT(cursorTime),
				transfer.And(
					transfer.CreateTimeEQ(cursorTime),
					transfer.IDLT(cursorID),
				),
			),
		).
		Order(transfer.ByCreateTime(entsql.OrderDesc()), transfer.ByID(entsql.OrderDesc())).
		Limit(batchSize).
		Select(transfer.FieldID, transfer.FieldCreateTime).
		Scan(ctx, &transfers)
	if err != nil {
		return 0, fmt.Errorf("failed to query transfers: %w", err)
	}

	if len(transfers) == 0 {
		logger.Info("no more transfers to process, processing complete")
		return 0, nil
	}

	// Mark the session dirty so the DatabaseMiddleware commits the transaction.
	// Raw ExecContext bypasses Ent's mutation layer and does not mark dirty automatically.
	ent.MarkTxDirty(ctx)

	totalRepaired := 0
	for _, t := range transfers {
		for _, table := range []string{"transfer_senders", "transfer_receivers"} {
			//nolint:forbidigo // Raw SQL required: create_time is Immutable in Ent schema and cannot be updated via generated builders.
			res, err := client.ExecContext(ctx,
				fmt.Sprintf(`UPDATE %s SET create_time = $1, update_time = NOW() WHERE transfer_id = $2`, table),
				t.CreateTime, t.ID,
			)
			if err != nil {
				return totalRepaired, fmt.Errorf("failed to update %s for transfer %s: %w", table, t.ID, err)
			}
			n, _ := res.RowsAffected()
			totalRepaired += int(n)
		}
	}

	// Advance cursor to the oldest transfer in this batch.
	oldest := transfers[len(transfers)-1]
	newCursor := repairCursor{UnixMicros: oldest.CreateTime.UnixMicro(), ID: oldest.ID.String()}
	if mc != nil {
		saveCursor(mc, cursorKey, newCursor)
	}

	logger.Sugar().Infof("processed %d participant records across %d transfers, now at %s",
		totalRepaired, len(transfers), oldest.CreateTime.UTC().Format(time.RFC3339))
	return totalRepaired, nil
}
