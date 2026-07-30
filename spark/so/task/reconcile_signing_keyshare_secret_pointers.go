package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/knobs"
)

// reconcileSigningKeyshareSecretPointersMu serialises scheduled ticks within a
// single SO process so a slow run can't overlap the next one and clobber its
// cursor write.
var reconcileSigningKeyshareSecretPointersMu sync.Mutex

const (
	// Keyshares updated inside the grace period are skipped. A rotation commits
	// the ephemeral secret before the main pointer, so a keyshare mid-rotation
	// can legitimately read as unresolvable across the two independent queries
	// this task issues.
	reconcileSigningKeyshareSecretPointersGracePeriod         = 10 * time.Minute
	reconcileSigningKeyshareSecretPointersDefaultBatchSize    = 1000
	reconcileSigningKeyshareSecretPointersDefaultMaxScanCount = 100_000
	reconcileSigningKeyshareSecretPointersCursorKeyPrefix     = "reconcile_signing_keyshare_secret_pointers_cursor"
	// Cap on per-keyshare warn logs in a single run. The count is what drives the
	// dashboard; the ids are for an operator following up, and a large population
	// should not be able to flood the log to get them.
	reconcileSigningKeyshareSecretPointersMaxLoggedPerRun = 50
)

// danglingSigningKeysharePointer is a main-DB keyshare whose secret_version does
// not resolve to a row in the ephemeral store.
type danglingSigningKeysharePointer struct {
	SigningKeyshareID uuid.UUID
	SecretVersion     int32
	// HasMainFallback reports whether the legacy main-DB secret_share column is
	// still populated. If it is, GetSecretShare still resolves and the keyshare
	// is usable today — but clear_signing_keyshare_secret_shares will remove that
	// column, at which point the keyshare becomes unusable.
	HasMainFallback bool
}

type reconcileSigningKeyshareSecretPointersResult struct {
	ScannedCount int
	Dangling     []danglingSigningKeysharePointer
	// NextCursor is the id to resume from on the next run, or nil if the scan
	// reached the end of the eligible data and should wrap to the oldest row.
	NextCursor *uuid.UUID
}

// runReconcileSigningKeyshareSecretPointers walks main-DB signing_keyshares in id
// order and reports every row whose secret_version does not resolve to a row in
// the ephemeral store. Such a keyshare cannot sign: GetSecretShare fails with
// ErrSigningKeyshareSecretMissing, and every flow that needs the share fails
// with it.
//
// This is the main-table counterpart to purge_dangling_signing_keyshare_secrets,
// which walks the ephemeral table. That direction can only see a broken keyshare
// that still has at least one stale ephemeral row to trip its missing_current
// guard — a keyshare whose ephemeral rows are all gone has nothing left in that
// table to scan, so the purge side is structurally blind to it. Scanning from the
// main table sees both shapes, which is what makes the population measurable.
//
// Read-only by design. Repair requires a per-keyshare decision — roll the pointer
// back to a surviving version, or reshare from the other operators — so this task
// only measures the population and leaves that choice to an operator.
func runReconcileSigningKeyshareSecretPointers(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
	sugar := logging.GetLoggerFromContext(ctx).Sugar()

	if !reconcileSigningKeyshareSecretPointersMu.TryLock() {
		sugar.Info("reconcile_signing_keyshare_secret_pointers: previous tick still running on this pod, skipping")
		return nil
	}
	defer reconcileSigningKeyshareSecretPointersMu.Unlock()

	if _, err := entephemeral.GetDbFromContext(ctx); err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			sugar.Info("reconcile_signing_keyshare_secret_pointers: ephemeral db not configured, skipping run")
			return nil
		}
		return fmt.Errorf("failed to get ephemeral db from context: %w", err)
	}

	batchSize := int(knobsService.GetValue(knobs.KnobReconcileSigningKeyshareSecretPointersBatchSize, reconcileSigningKeyshareSecretPointersDefaultBatchSize))
	if batchSize <= 0 {
		sugar.Warnf("reconcile_signing_keyshare_secret_pointers: invalid batchSize %d (knob %s), skipping run", batchSize, knobs.KnobReconcileSigningKeyshareSecretPointersBatchSize)
		return nil
	}
	maxScanCount := int(knobsService.GetValue(knobs.KnobReconcileSigningKeyshareSecretPointersMaxScanCount, reconcileSigningKeyshareSecretPointersDefaultMaxScanCount))
	if maxScanCount <= 0 {
		sugar.Warnf("reconcile_signing_keyshare_secret_pointers: invalid maxScanCount %d (knob %s), skipping run", maxScanCount, knobs.KnobReconcileSigningKeyshareSecretPointersMaxScanCount)
		return nil
	}

	mc, cacheErr := newScanCursorMemcacheClient(config.CacheURI)
	if cacheErr != nil {
		// Not fatal: a nil client just means every run restarts at the oldest row.
		// Worth a warning because that silently costs coverage on a table too large
		// to scan in one run.
		sugar.Warnf("reconcile_signing_keyshare_secret_pointers: cursor cache unavailable, each run will restart from the oldest row: %v", cacheErr)
	}
	cursorKey := scanCursorKey(reconcileSigningKeyshareSecretPointersCursorKeyPrefix, config.Index)
	startCursor := loadScanCursor(mc, cursorKey)

	cutoffTime := time.Now().Add(-reconcileSigningKeyshareSecretPointersGracePeriod)
	result, err := reconcileSigningKeyshareSecretPointersScan(ctx, cutoffTime, batchSize, maxScanCount, startCursor)
	if err != nil {
		return err
	}

	if result.NextCursor != nil {
		if cacheErr := saveScanCursor(mc, cursorKey, *result.NextCursor); cacheErr != nil {
			sugar.Warnf("reconcile_signing_keyshare_secret_pointers: failed to persist cursor (will resume from previous cursor or start over on next run): %v", cacheErr)
		}
	} else {
		if cacheErr := deleteScanCursor(mc, cursorKey); cacheErr != nil {
			sugar.Warnf("reconcile_signing_keyshare_secret_pointers: failed to clear cursor at end of pass (next run may rescan from stale cursor): %v", cacheErr)
		}
	}

	reportDanglingSigningKeysharePointers(ctx, result)
	return nil
}

func reportDanglingSigningKeysharePointers(ctx context.Context, result reconcileSigningKeyshareSecretPointersResult) {
	sugar := logging.GetLoggerFromContext(ctx).Sugar()

	unusableCount := 0
	for i, dangling := range result.Dangling {
		if !dangling.HasMainFallback {
			unusableCount++
		}
		switch {
		case i < reconcileSigningKeyshareSecretPointersMaxLoggedPerRun:
			sugar.Warnf(
				"reconcile_signing_keyshare_secret_pointers: signing keyshare %s references missing ephemeral secret version %d (main secret_share fallback present: %t)",
				dangling.SigningKeyshareID,
				dangling.SecretVersion,
				dangling.HasMainFallback,
			)
		case i == reconcileSigningKeyshareSecretPointersMaxLoggedPerRun:
			sugar.Warnf(
				"reconcile_signing_keyshare_secret_pointers: %d further dangling signing keyshares this run not logged individually",
				len(result.Dangling)-reconcileSigningKeyshareSecretPointersMaxLoggedPerRun,
			)
		}
	}

	recordSigningKeysharePointerReconciliationOutcome(ctx, "dangling", unusableCount)
	recordSigningKeysharePointerReconciliationOutcome(ctx, "dangling_with_main_fallback", len(result.Dangling)-unusableCount)
	if result.NextCursor == nil {
		// Emitted only when a scan walks off the end of the eligible rows, so it
		// means "the population was fully covered as of this run".
		//
		// It doubles as cursor evidence only when the eligible population exceeds
		// the per-run scan cap, because only then does a pass need more than one
		// run to finish: a cursor that is not persisting restarts every run at the
		// oldest row, so no run reaches the end and this flattens to zero. Below
		// the cap every run covers everything and this increments whether or not
		// the cursor works — correctly, since coverage genuinely is complete and
		// the cursor is not load-bearing at that size.
		recordSigningKeysharePointerReconciliationOutcome(ctx, "pass_complete", 1)
	}

	if result.NextCursor != nil {
		sugar.Infof(
			"reconcile_signing_keyshare_secret_pointers: scanned %d eligible signing keyshares, %d with dangling secret pointers (%d unusable now, %d still covered by the main secret_share fallback); budget exhausted, cursor advanced to id=%s",
			result.ScannedCount,
			len(result.Dangling),
			unusableCount,
			len(result.Dangling)-unusableCount,
			result.NextCursor,
		)
		return
	}
	sugar.Infof(
		"reconcile_signing_keyshare_secret_pointers: scanned %d eligible signing keyshares, %d with dangling secret pointers (%d unusable now, %d still covered by the main secret_share fallback); scan reached end of eligible data, cursor reset",
		result.ScannedCount,
		len(result.Dangling),
		unusableCount,
		len(result.Dangling)-unusableCount,
	)
}

func reconcileSigningKeyshareSecretPointersScan(
	ctx context.Context,
	cutoffTime time.Time,
	batchSize int,
	maxScanCount int,
	startCursor *uuid.UUID,
) (reconcileSigningKeyshareSecretPointersResult, error) {
	ephemeralDB, err := entephemeral.GetDbFromContext(ctx)
	if err != nil {
		return reconcileSigningKeyshareSecretPointersResult{}, fmt.Errorf("failed to get ephemeral db from context: %w", err)
	}

	var result reconcileSigningKeyshareSecretPointersResult
	cursor := startCursor
	reachedEndOfEligibleData := false

	for result.ScannedCount < maxScanCount {
		queryLimit := min(batchSize, maxScanCount-result.ScannedCount)

		page, err := queryEligibleSigningKeysharePage(ctx, cutoffTime, queryLimit, cursor)
		if err != nil {
			return reconcileSigningKeyshareSecretPointersResult{}, err
		}
		if len(page) == 0 {
			reachedEndOfEligibleData = true
			break
		}
		result.ScannedCount += len(page)

		if err := endSigningKeysharePointerScanRead(ctx); err != nil {
			return reconcileSigningKeyshareSecretPointersResult{}, err
		}

		dangling, err := findDanglingSigningKeysharePointers(ctx, ephemeralDB, page)
		if err != nil {
			return reconcileSigningKeyshareSecretPointersResult{}, err
		}
		result.Dangling = append(result.Dangling, dangling...)

		cursor = new(page[len(page)-1].ID)
		if len(page) < queryLimit {
			reachedEndOfEligibleData = true
			break
		}
	}

	if !reachedEndOfEligibleData {
		result.NextCursor = cursor
	}
	return result, nil
}

func queryEligibleSigningKeysharePage(
	ctx context.Context,
	cutoffTime time.Time,
	queryLimit int,
	cursor *uuid.UUID,
) ([]*ent.SigningKeyshare, error) {
	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get main db from context: %w", err)
	}

	query := mainDB.SigningKeyshare.Query().
		Where(
			signingkeyshare.SecretVersionNotNil(),
			signingkeyshare.UpdateTimeLT(cutoffTime),
		).
		Order(signingkeyshare.ByID(sql.OrderAsc())).
		Limit(queryLimit)
	if cursor != nil {
		query = query.Where(signingkeyshare.IDGT(*cursor))
	}

	// secret_share is deliberately left out of the projection: this task never
	// needs the plaintext secret, only whether the column is populated, which
	// confirmDanglingSigningKeysharePointers resolves without loading the bytes.
	page, err := query.
		Select(signingkeyshare.FieldID, signingkeyshare.FieldSecretVersion).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query signing keyshares with a secret version: %w", err)
	}
	return page, nil
}

// endSigningKeysharePointerScanRead closes the main-DB transaction the page read
// ran in. Nothing here writes, so this is about snapshot freshness rather than
// durability: `ent.GetDbFromContext` hands back a transaction-bound client, and
// the confirmation re-read must observe the ephemeral existence check's aftermath.
// A transaction that began before that check is only guaranteed to under READ
// COMMITTED, which is the driver default but is not pinned anywhere — so the
// transaction is ended here instead of relying on it. Bounding transaction
// lifetime to a page rather than a whole 100k-row scan is a welcome side effect.
func endSigningKeysharePointerScanRead(ctx context.Context) error {
	if ent.IsReadOnlySession(ctx) {
		// Read-only sessions issue every query on the bare client, so each one
		// already gets its own snapshot and there is no transaction to end.
		return nil
	}
	if err := ent.DbCommit(ctx); err != nil {
		return fmt.Errorf("failed to end signing keyshare page read transaction: %w", err)
	}
	return nil
}

func findDanglingSigningKeysharePointers(
	ctx context.Context,
	ephemeralDB *entephemeral.Client,
	page []*ent.SigningKeyshare,
) ([]danglingSigningKeysharePointer, error) {
	keysharesByID := make(map[uuid.UUID]*ent.SigningKeyshare, len(page))
	for _, keyshare := range page {
		keysharesByID[keyshare.ID] = keyshare
	}

	secretExists, err := getCurrentSigningKeyshareSecretExistence(ctx, ephemeralDB, keysharesByID)
	if err != nil {
		return nil, err
	}

	candidateIDs := make([]uuid.UUID, 0, len(page))
	for _, keyshare := range page {
		if !secretExists[keyshare.ID] {
			candidateIDs = append(candidateIDs, keyshare.ID)
		}
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}
	return confirmDanglingSigningKeysharePointers(ctx, keysharesByID, candidateIDs)
}

// confirmDanglingSigningKeysharePointers re-reads the candidates' pointers and
// drops any that moved.
//
// The page read and the ephemeral existence check are separate queries against
// separate databases, so a rotation that commits between them looks identical to
// the bug this task hunts: the pointer from the page read names a version the
// commit hook has since retired. Re-reading distinguishes the two — a moved
// pointer means the keyshare rotated and is healthy at its new version.
//
// This is only sound because the caller ended the page read's transaction first,
// so the client fetched below opens a new one. Do not hoist this client up to the
// caller and pass it in: a client captured before that commit would either be
// bound to a closed transaction or, at a higher isolation level, still be reading
// the snapshot that produced the stale pointer.
func confirmDanglingSigningKeysharePointers(
	ctx context.Context,
	observed map[uuid.UUID]*ent.SigningKeyshare,
	candidateIDs []uuid.UUID,
) ([]danglingSigningKeysharePointer, error) {
	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get main db from context: %w", err)
	}

	current, err := mainDB.SigningKeyshare.Query().
		Where(signingkeyshare.IDIn(candidateIDs...)).
		Select(signingkeyshare.FieldID, signingkeyshare.FieldSecretVersion).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to re-read signing keyshare secret versions: %w", err)
	}

	// This second statement feeds HasMainFallback only — never the dangling
	// verdict, which the loop below decides purely from the pointer re-read
	// above. A rotation committing between the two therefore cannot turn a
	// healthy keyshare into a reported one; it can only move an already-dangling
	// keyshare between the two outcome buckets, whose total is the same.
	//
	// The realistic writer here is clear_signing_keyshare_secret_shares. If it
	// clears the column between the two statements this reports `dangling`
	// rather than `dangling_with_main_fallback`, which is the louder of the two
	// buckets — so the split biases toward over-reporting severity, not under.
	fallbackIDs, err := mainDB.SigningKeyshare.Query().
		Where(
			signingkeyshare.IDIn(candidateIDs...),
			signingkeyshare.SecretShareNotNil(),
		).
		IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query main secret_share fallback for signing keyshares: %w", err)
	}
	hasMainFallback := make(map[uuid.UUID]bool, len(fallbackIDs))
	for _, id := range fallbackIDs {
		hasMainFallback[id] = true
	}

	confirmed := make([]danglingSigningKeysharePointer, 0, len(current))
	for _, keyshare := range current {
		observedKeyshare, ok := observed[keyshare.ID]
		if !ok || observedKeyshare.SecretVersion == nil {
			continue
		}
		if keyshare.SecretVersion == nil || *keyshare.SecretVersion != *observedKeyshare.SecretVersion {
			continue
		}
		confirmed = append(confirmed, danglingSigningKeysharePointer{
			SigningKeyshareID: keyshare.ID,
			SecretVersion:     *keyshare.SecretVersion,
			HasMainFallback:   hasMainFallback[keyshare.ID],
		})
	}
	return confirmed, nil
}
