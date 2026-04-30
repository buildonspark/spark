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

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"go.uber.org/zap"
)

const (
	retypeSSPCompensationDefaultBatchSize = 100
	// Bump the version suffix to invalidate stale cursors and force a restart from the seed.
	retypeCursorKeyPrefix = "retype_ssp_compensation_cursor_v1"
	// Buffer subtracted from the task deadline so we can finish the current batch,
	// save the cursor, and return gracefully before the middleware cancels us.
	retypeSoftDeadlineBuffer = 30 * time.Second
	// Fallback soft-deadline duration when no context deadline is attached.
	retypeDefaultSoftDeadline = 4 * time.Minute

	retypePhaseRunning = 1
	retypePhaseDone    = 2
)

// retypeSSPCompensationTimeout caps the middleware-applied deadline for a single
// invocation. An invocation typically performs at most one update-producing batch
// (~2s) plus any leading dry batches; the 5-minute ceiling is a safety net for
// long stretches of dry scanning, not the expected run time.
var retypeSSPCompensationTimeout = 5 * time.Minute

// sspPubkeys is the hardcoded set of production SSP identity public keys.
// Sourced from webdev/dbt_project.yml. Both mainnet and regtest SSP pubkeys
// coexist on the prod database cluster, so both must be scanned.
var sspPubkeys = []keys.Public{
	keys.MustParsePublicKeyHex("023e33e2920326f64ea31058d44777442d97d7d5cbfcf54e3060bc1695e5261c93"), // mainnet prod
	keys.MustParsePublicKeyHex("022bf283544b16c0622daecb79422007d167eca6ce9f0c98c0c49833b1f7170bfe"), // regtest prod
}

// retypeMu guards against in-pod scheduler overlap while a long invocation is still running.
// Cross-pod overlap during rolling deploys is tolerated because UPDATEs are idempotent
// (filter includes type='TRANSFER') and the cursor is last-write-wins.
var retypeMu sync.Mutex

type retypeCursor struct {
	CreateTimeMicros int64
	ID               string
	Phase            int
}

func (c retypeCursor) String() string {
	return fmt.Sprintf("%d:%s:%d", c.CreateTimeMicros, c.ID, c.Phase)
}

func parseRetypeCursor(raw string) (retypeCursor, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return retypeCursor{}, false
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return retypeCursor{}, false
	}
	phase, err := strconv.Atoi(parts[2])
	if err != nil || phase < retypePhaseRunning || phase > retypePhaseDone {
		return retypeCursor{}, false
	}
	return retypeCursor{
		CreateTimeMicros: micros,
		ID:               parts[1],
		Phase:            phase,
	}, true
}

func retypeCursorKey(operatorIndex uint64) string {
	return fmt.Sprintf("%s:%d", retypeCursorKeyPrefix, operatorIndex)
}

func newRetypeMemcacheClient(cacheURI string) *memcache.Client {
	addr := strings.TrimPrefix(cacheURI, "memcaches://")
	addr = strings.TrimPrefix(addr, "memcache://")
	mc := memcache.New(addr)
	mc.Timeout = 2 * time.Second
	return mc
}

// loadRetypeCursor returns the cached cursor, whether one was found, and any
// memcache transport error. A cache miss (or nil client) returns (_, false, nil);
// a non-nil error indicates a real memcache failure worth logging.
func loadRetypeCursor(mc *memcache.Client, key string) (retypeCursor, bool, error) {
	if mc == nil {
		return retypeCursor{}, false, nil
	}
	item, err := mc.Get(key)
	if err != nil {
		if errors.Is(err, memcache.ErrCacheMiss) {
			return retypeCursor{}, false, nil
		}
		return retypeCursor{}, false, err
	}
	cursor, ok := parseRetypeCursor(string(item.Value))
	return cursor, ok, nil
}

func saveRetypeCursor(mc *memcache.Client, key string, cursor retypeCursor) error {
	if mc == nil {
		return nil
	}
	return mc.Set(&memcache.Item{
		Key:        key,
		Value:      []byte(cursor.String()),
		Expiration: 14 * 24 * 3600, // 14 days
	})
}

// seedRetypeCursor returns a cursor positioned before any real row so the first
// batch picks up the oldest candidates. The underlying query is a parallel seq
// scan regardless of cursor value, so seeding at zero time costs nothing extra.
func seedRetypeCursor() retypeCursor {
	return retypeCursor{
		CreateTimeMicros: 0,
		ID:               "00000000-0000-0000-0000-000000000000",
		Phase:            retypePhaseRunning,
	}
}

// retypeSSPCompensationTransfers finds TRANSFER-type transfers sent by the SSP
// as compensation for failed swaps and retypes them to COUNTER_SWAP.
//
// Background: historically, when an SSP swap failed after the primary swap was
// sender-key-tweaked, the SSP would send a raw spark-to-spark TRANSFER to the
// client as compensation. These show up as unexpected incoming transfers in the
// user's transaction history. getSSPCounterSwapFilter hides them at query time,
// but that approach has performance issues for high-volume wallets (see SP-2727).
// This task fixes the data so the filter can be removed.
//
// Identification: a transfer is an SSP compensation transfer if:
//   - type = TRANSFER
//   - sender_identity_pubkey is a known SSP
//   - the receiver had a SWAP/PRIMARY_SWAP_V3 with that SSP (receiver was swap's sender)
//
// Design notes:
//   - Hardcoded SSP pubkeys (instead of discovery by sampling) so regtest SSP isn't
//     missed when mainnet traffic dominates recent swaps.
//   - Processes at most one update-producing batch per invocation and returns, to
//     bound rollback blast radius: if the middleware's commit fails, only that one
//     batch's work is lost rather than dozens of batches accumulated in-memory.
//     Dry batches (no qualifying rows) still advance the cursor and continue scanning
//     until an update batch runs or the scan completes; they have no writes to lose.
//   - Uses keyset pagination on (create_time, id). The query's plan on prod is a
//     parallel seq scan (~1-2s warm), which is deterministic and bounded regardless
//     of cursor value — the cursor exists so the query skips candidates that failed
//     swap verification on a prior pass.
//   - Cursor is saved to memcache exactly once at the single function exit, keeping
//     the cursor advance atomic with the middleware-managed transaction commit.
//   - Terminates permanently via phaseDone marker in memcache once the scan returns
//     an empty batch.
//
// Gating is handled by the generic spark.so.task.enabled@retype_ssp_compensation knob
// via the task middleware; no task-specific gating knob is needed. Batch size is
// tunable at runtime via spark.so.retype_ssp_compensation_batch_size (default 100).
func retypeSSPCompensationTransfers(ctx context.Context, config *so.Config, client *ent.Client, batchSize int) (int, error) {
	if !retypeMu.TryLock() {
		return 0, nil
	}
	defer retypeMu.Unlock()

	logger := logging.GetLoggerFromContext(ctx).With(zap.String("task.name", "retype_ssp_compensation"))
	sugar := logger.Sugar()

	var mc *memcache.Client
	if config.CacheURI != "" {
		mc = newRetypeMemcacheClient(config.CacheURI)
	}
	cursorKey := retypeCursorKey(config.Index)

	cursor, hasCursor, err := loadRetypeCursor(mc, cursorKey)
	if err != nil {
		sugar.Warnf("failed to load retype cursor: %v", err)
	}
	if !hasCursor {
		cursor = seedRetypeCursor()
		sugar.Infof("seeded retype cursor: %s", cursor)
	} else {
		sugar.Infof("loaded retype cursor: %s", cursor)
	}

	if cursor.Phase == retypePhaseDone {
		sugar.Info("retype_ssp_compensation already complete, no-op")
		return 0, nil
	}

	softDeadline := time.Now().Add(retypeDefaultSoftDeadline)
	if deadline, ok := ctx.Deadline(); ok {
		softDeadline = deadline.Add(-retypeSoftDeadlineBuffer)
	}

	total := 0
	for time.Now().Before(softDeadline) {
		cursorTime := time.UnixMicro(cursor.CreateTimeMicros)
		cursorID, err := uuid.Parse(cursor.ID)
		if err != nil {
			return total, fmt.Errorf("invalid cursor ID %q: %w", cursor.ID, err)
		}

		candidates, err := client.Transfer.Query().
			Where(
				transfer.TypeEQ(st.TransferTypeTransfer),
				transfer.SenderIdentityPubkeyIn(sspPubkeys...),
				transfer.Or(
					transfer.CreateTimeGT(cursorTime),
					transfer.And(
						transfer.CreateTimeEQ(cursorTime),
						transfer.IDGT(cursorID),
					),
				),
			).
			Order(
				transfer.ByCreateTime(entsql.OrderAsc()),
				transfer.ByID(entsql.OrderAsc()),
			).
			Limit(batchSize).
			All(ctx)
		if err != nil {
			return total, fmt.Errorf("failed to query candidate transfers: %w", err)
		}

		if len(candidates) == 0 {
			cursor.Phase = retypePhaseDone
			sugar.Info("no more candidates, marking retype complete")
			break
		}

		// Batch-verify on the full (wallet, SSP, network) tuple: each candidate
		// retype requires the candidate's receiver to have sent a swap to the
		// same SSP that sent the candidate transfer, on the same network. This
		// mirrors getSSPCounterSwapFilter's implicit pair match and prevents
		// cross-SSP or cross-network false positives.
		receiverPKs := make([]keys.Public, 0, len(candidates))
		seen := make(map[string]bool)
		for _, t := range candidates {
			hex := t.ReceiverIdentityPubkey.ToHex()
			if !seen[hex] {
				seen[hex] = true
				receiverPKs = append(receiverPKs, t.ReceiverIdentityPubkey)
			}
		}

		verifiedSwaps, err := client.Transfer.Query().
			Where(
				transfer.TypeIn(st.TransferTypeSwap, st.TransferTypePrimarySwapV3),
				transfer.SenderIdentityPubkeyIn(receiverPKs...),
				transfer.ReceiverIdentityPubkeyIn(sspPubkeys...),
			).
			Select(
				transfer.FieldSenderIdentityPubkey,
				transfer.FieldReceiverIdentityPubkey,
				transfer.FieldNetwork,
			).
			All(ctx)
		if err != nil {
			return total, fmt.Errorf("failed to batch-verify swaps: %w", err)
		}

		type swapKey struct {
			wallet  string
			ssp     string
			network btcnetwork.Network
		}
		verifiedSet := make(map[swapKey]bool, len(verifiedSwaps))
		for _, s := range verifiedSwaps {
			verifiedSet[swapKey{
				wallet:  s.SenderIdentityPubkey.ToHex(),
				ssp:     s.ReceiverIdentityPubkey.ToHex(),
				network: s.Network,
			}] = true
		}

		var toUpdate []uuid.UUID
		for _, t := range candidates {
			key := swapKey{
				wallet:  t.ReceiverIdentityPubkey.ToHex(),
				ssp:     t.SenderIdentityPubkey.ToHex(),
				network: t.Network,
			}
			if verifiedSet[key] {
				toUpdate = append(toUpdate, t.ID)
			}
		}

		// Advance cursor in memory to the last candidate in this batch regardless
		// of whether we end up updating. Dry batches (no qualifying rows) still
		// count as forward progress, so skipping them on resume is correct.
		last := candidates[len(candidates)-1]
		cursor = retypeCursor{
			CreateTimeMicros: last.CreateTime.UnixMicro(),
			ID:               last.ID.String(),
			Phase:            retypePhaseRunning,
		}

		if len(toUpdate) > 0 {
			updated, err := client.Transfer.Update().
				Where(
					transfer.IDIn(toUpdate...),
					transfer.TypeEQ(st.TransferTypeTransfer),
				).
				SetType(st.TransferTypeCounterSwap).
				Save(ctx)
			if err != nil {
				return total, fmt.Errorf("failed to update transfers: %w", err)
			}
			total += updated
			idStrs := make([]string, len(toUpdate))
			for i, id := range toUpdate {
				idStrs[i] = id.String()
			}
			sugar.Infof("retyped %d/%d candidates in batch to COUNTER_SWAP; attempted ids=[%s]", updated, len(candidates), strings.Join(idStrs, ","))
			// Exit after one update-producing batch to bound rollback blast radius.
			// If the middleware commit fails, only this batch's work is lost; if
			// anything downstream errors we haven't advanced past uncommitted rows.
			// Dry batches don't exit early because they have no writes to lose.
			break
		}

		sugar.Infof("scanned %d candidates in batch, none qualified", len(candidates))
	}

	if err := saveRetypeCursor(mc, cursorKey, cursor); err != nil {
		sugar.Warnf("failed to save retype cursor: %v", err)
	}
	sugar.Infof("invocation complete, total %d retyped, cursor at %s", total, cursor)
	return total, nil
}
