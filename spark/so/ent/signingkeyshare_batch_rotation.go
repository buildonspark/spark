package ent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/entephemeral"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"
)

// SigningKeyshareTweak describes one keyshare rotation input for
// TweakSigningKeyshares. Keyshare must come from a row-locked read in the
// current transaction and is not mutated; the rotated state is returned as a
// new entity.
type SigningKeyshareTweak struct {
	Keyshare       *SigningKeyshare
	SecretTweak    keys.Private
	PubKeyTweak    keys.Public
	PubSharesTweak map[string]keys.Public
}

// KeyshareRotationError attributes a batch rotation failure to the specific
// keyshare that caused it, so callers can map it back to their own domain
// object (e.g. the leaf being claimed).
type KeyshareRotationError struct {
	KeyshareID uuid.UUID
	Err        error
}

func (e *KeyshareRotationError) Error() string {
	return fmt.Sprintf("keyshare %s: %v", e.KeyshareID, e.Err)
}

func (e *KeyshareRotationError) Unwrap() error { return e.Err }

// signingKeyshareSecretVersionRef identifies one ephemeral secret version row
// for cleanup.
type signingKeyshareSecretVersionRef struct {
	signingKeyshareID uuid.UUID
	version           int32
}

// signingKeyshareRotationItem carries one keyshare's computed rotation state
// through the batch pipeline.
type signingKeyshareRotationItem struct {
	tweak      *SigningKeyshareTweak
	newSecret  keys.Private
	newPubKey  keys.Public
	newShares  map[string]keys.Public
	newVersion int32
}

// batchRotationOutcome tracks, per keyshare, how far the batch rotation got
// so the commit/rollback hooks retire exactly the right ephemeral versions.
// Same cleanup contract as the single-row rotation (see
// prepareSigningKeyshareSecretRotation): cleanup never re-reads main state.
type batchRotationOutcome struct {
	commitAttempted bool
	newVersions     map[uuid.UUID]int32
	retiredBase     map[uuid.UUID]int32
	mainSaved       map[uuid.UUID]bool
}

// TweakSigningKeyshares is the batched equivalent of running
// SigningKeyshare.TweakKeyShare over every entry: it applies each tweak to
// its keyshare's secret/public material and persists all rotations with O(1)
// ephemeral round trips (one batched advisory-lock statement, one
// latest-version read, one bulk INSERT, one commit, and batched
// commit/rollback cleanup) plus one CAS-guarded main-row UPDATE per keyshare
// with no post-UPDATE refetch.
//
// Requirements mirror the single-row path: every Keyshare MUST come from a
// row-locked (ForUpdate) read in the current transaction — the CAS uses its
// SecretVersion as the expected base, and this cannot be enforced here.
// Correctness survives an unlocked caller (a lost CAS aborts instead of
// corrupting), but availability does not: one stale entry fails the WHOLE
// batch with AbortedConcurrentKeyshareRotation, so under contention an
// unlocked caller turns a single-row retry into a full-batch retry. The
// whole batch shares one frozen dual-write decision. After a CAS miss the
// surrounding transaction must roll back, which retires every ephemeral
// version this call created.
//
// The returned entities carry the rotated key material and version, but
// their CreateTime/UpdateTime are the pre-rotation read-time values (the
// persisted update_time is newer) and they are not ent-scanned rows — reload
// from the DB before marshaling one to a client.
func TweakSigningKeyshares(ctx context.Context, tweaks []*SigningKeyshareTweak) (map[uuid.UUID]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.TweakSigningKeyshares")
	defer span.End()

	if len(tweaks) == 0 {
		return map[uuid.UUID]*SigningKeyshare{}, nil
	}
	ctx = FreezeSigningKeyshareSecretDualWriteDecision(ctx)

	keyshares := make([]*SigningKeyshare, 0, len(tweaks))
	seen := make(map[uuid.UUID]struct{}, len(tweaks))
	for _, tweak := range tweaks {
		if tweak == nil || tweak.Keyshare == nil {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("nil keyshare tweak in batch"))
		}
		if _, dup := seen[tweak.Keyshare.ID]; dup {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("duplicate keyshare %s in tweak batch", tweak.Keyshare.ID))
		}
		seen[tweak.Keyshare.ID] = struct{}{}
		keyshares = append(keyshares, tweak.Keyshare)
	}

	if err := HydrateSigningKeyshareSecrets(ctx, keyshares); err != nil {
		// Soft-fail like lockAndHydrateLeafKeyshares: the per-keyshare
		// GetSecretShare below re-attempts resolution and surfaces the precise
		// per-keyshare error (including the keyshare ID) instead of a
		// batch-stage failure.
		logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Warn(
			"batch keyshare secret hydration failed during batched tweak, falling back to per-keyshare resolution")
	}

	items := make([]*signingKeyshareRotationItem, 0, len(tweaks))
	for _, tweak := range tweaks {
		sk := tweak.Keyshare
		secretShare, err := sk.GetSecretShare(ctx)
		if err != nil {
			if concurrentErr := sk.concurrentRotationErrorIfVersionMoved(ctx, err); concurrentErr != nil {
				err = concurrentErr
			}
			return nil, &KeyshareRotationError{KeyshareID: sk.ID, Err: err}
		}
		newShares := make(map[string]keys.Public, len(sk.PublicShares))
		for id, pubShare := range sk.PublicShares {
			newShares[id] = pubShare.Add(tweak.PubSharesTweak[id])
		}
		items = append(items, &signingKeyshareRotationItem{
			tweak:     tweak,
			newSecret: secretShare.Add(tweak.SecretTweak),
			newPubKey: sk.PublicKey.Add(tweak.PubKeyTweak),
			newShares: newShares,
		})
	}

	useEphemeral, outcome, err := prepareBatchSecretRotations(ctx, items)
	if err != nil {
		return nil, err
	}

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	updated := make(map[uuid.UUID]*SigningKeyshare, len(items))
	dualWrite := shouldDualWriteSigningKeyshareSecret(ctx)
	for _, item := range items {
		sk := item.tweak.Keyshare
		update := db.SigningKeyshare.Update().
			SetPublicKey(item.newPubKey).
			SetPublicShares(item.newShares)
		if sk.SecretVersion != nil {
			update = update.Where(signingkeyshare.IDEQ(sk.ID), signingkeyshare.SecretVersionEQ(*sk.SecretVersion))
		} else {
			update = update.Where(signingkeyshare.IDEQ(sk.ID), signingkeyshare.SecretVersionIsNil())
		}
		if useEphemeral {
			update = update.SetSecretVersion(item.newVersion)
		} else {
			update = update.ClearSecretVersion()
		}
		if !useEphemeral || dualWrite {
			update = update.SetSecretShare(item.newSecret)
		} else {
			update = update.ClearSecretShare()
		}

		if outcome != nil && sk.SecretVersion != nil {
			outcome.retiredBase[sk.ID] = *sk.SecretVersion
		}
		// Predicate-style Update (not UpdateOne) reports affected rows without
		// ent's post-UPDATE entity refetch; zero rows means the CAS lost.
		affected, err := update.Save(ctx)
		if err != nil {
			return nil, &KeyshareRotationError{KeyshareID: sk.ID, Err: err}
		}
		if affected == 0 {
			return nil, &KeyshareRotationError{KeyshareID: sk.ID, Err: sparkerrors.AbortedConcurrentKeyshareRotation(fmt.Errorf(
				"signing keyshare %s secret_version moved from expected base; retry from a fresh read",
				sk.ID,
			))}
		}
		if outcome != nil {
			outcome.mainSaved[sk.ID] = true
		}

		// SigningKeyshare embeds a mutex, so build the rotated view explicitly
		// instead of copying the struct.
		rotated := &SigningKeyshare{
			config:           sk.config,
			ID:               sk.ID,
			CreateTime:       sk.CreateTime,
			UpdateTime:       sk.UpdateTime,
			Status:           sk.Status,
			PublicShares:     item.newShares,
			PublicKey:        item.newPubKey,
			MinSigners:       sk.MinSigners,
			CoordinatorIndex: sk.CoordinatorIndex,
		}
		if useEphemeral {
			version := item.newVersion
			rotated.SecretVersion = &version
		}
		if !useEphemeral || dualWrite {
			newSecret := item.newSecret
			rotated.SecretShare = &newSecret
		}
		newSecret := item.newSecret
		rotated.ExternalSecret = &newSecret
		updated[sk.ID] = rotated
	}

	return updated, nil
}

// prepareBatchSecretRotations writes every rotation's next ephemeral secret
// version in one batched transaction and registers a single pair of
// commit/rollback hooks on the main transaction that retire exactly the right
// versions per keyshare. Returns useEphemeral=false (and a nil outcome) when
// no ephemeral store is configured, in which case the caller falls back to
// main-DB-only writes. On success it fills in item.newVersion for every item.
func prepareBatchSecretRotations(ctx context.Context, items []*signingKeyshareRotationItem) (bool, *batchRotationOutcome, error) {
	ephemeralTx, err := entephemeral.GetTxFromContext(ctx)
	if err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			return false, nil, nil
		}
		return false, nil, err
	}
	defer func() { _ = ephemeralTx.Rollback() }()

	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.tweak.Keyshare.ID)
	}
	latest, err := entephemeral.GetLatestSigningKeyshareSecretVersionsForUpdate(ctx, ids)
	if err != nil {
		return false, nil, err
	}

	inserts := make([]entephemeral.SigningKeyshareSecretVersionInput, 0, len(items))
	for _, item := range items {
		id := item.tweak.Keyshare.ID
		var ephemeralLatest *int32
		if row, ok := latest[id]; ok {
			version := row.Version
			ephemeralLatest = &version
		}
		newVersion, err := nextSigningKeyshareSecretVersion(id, ephemeralLatest, item.tweak.Keyshare.SecretVersion)
		if err != nil {
			return false, nil, err
		}
		item.newVersion = newVersion
		inserts = append(inserts, entephemeral.SigningKeyshareSecretVersionInput{
			SigningKeyshareID: id,
			Version:           item.newVersion,
			SecretShare:       item.newSecret,
		})
	}
	if err := entephemeral.CreateSigningKeyshareSecretVersionsLocked(ctx, inserts); err != nil {
		return false, nil, err
	}
	if err := ephemeralTx.Commit(); err != nil {
		return false, nil, err
	}

	outcome := &batchRotationOutcome{
		newVersions: make(map[uuid.UUID]int32, len(items)),
		retiredBase: make(map[uuid.UUID]int32, len(items)),
		mainSaved:   make(map[uuid.UUID]bool, len(items)),
	}
	for _, item := range items {
		outcome.newVersions[item.tweak.Keyshare.ID] = item.newVersion
	}

	// Cleanup must not inherit request cancellation, or rollback/commit hooks
	// can leave orphaned versions.
	cleanupCtx := context.WithoutCancel(ctx)

	tx, err := GetTxFromContext(ctx)
	if err != nil {
		deleteSigningKeyshareSecretVersionRefsBestEffort(cleanupCtx, refsForAll(outcome.newVersions), "main tx unavailable after batched ephemeral commit")
		return false, nil, err
	}

	tx.OnRollback(func(fn Rollbacker) Rollbacker {
		return RollbackFunc(func(ctx context.Context, tx *Tx) error {
			err := fn.Rollback(ctx, tx)
			if !outcome.commitAttempted && deleteSigningKeyshareSecretVersionRefsBestEffort(cleanupCtx, refsForAll(outcome.newVersions), "main tx rollback") {
				recordSigningKeyshareSecretCleanupOutcome(cleanupCtx, "rollback_deleted_new")
			}
			return err
		})
	})

	tx.OnCommit(func(fn Committer) Committer {
		return CommitFunc(func(ctx context.Context, tx *Tx) error {
			outcome.commitAttempted = true
			err := fn.Commit(ctx, tx)
			if err != nil {
				recordSigningKeyshareSecretCleanupOutcome(cleanupCtx, "ambiguous_commit_preserved")
				logging.GetLoggerFromContext(cleanupCtx).With(zap.Error(err)).Sugar().Warnf(
					"preserving %d batched signing keyshare secret versions after ambiguous main tx commit",
					len(outcome.newVersions),
				)
				return err
			}
			var retire, purge []signingKeyshareSecretVersionRef
			for id, newVersion := range outcome.newVersions {
				base, hasBase := outcome.retiredBase[id]
				switch {
				case !outcome.mainSaved[id]:
					// The main row never pointed at this new version, so it is
					// an unreachable orphan. Unreachable for the current
					// all-or-nothing caller (a CAS miss errors out and the tx
					// rolls back, so OnCommit never fires with a partial
					// mainSaved map) — kept for contract symmetry with the
					// single-row hook; re-verify before adding a
					// continue-on-error caller.
					purge = append(purge, signingKeyshareSecretVersionRef{signingKeyshareID: id, version: newVersion})
				case !hasBase:
					// Nil-base winners have no prior row to retire.
				case base == newVersion:
					recordSigningKeyshareSecretRetireCollision(cleanupCtx, id, newVersion)
				default:
					// A committed CAS winner permanently unreferences its base
					// version.
					retire = append(retire, signingKeyshareSecretVersionRef{signingKeyshareID: id, version: base})
				}
			}
			if len(retire) > 0 && deleteSigningKeyshareSecretVersionRefsBestEffort(cleanupCtx, retire, "main tx commit") {
				recordSigningKeyshareSecretCleanupOutcome(cleanupCtx, "commit_deleted_base")
			}
			if len(purge) > 0 && deleteSigningKeyshareSecretVersionRefsBestEffort(cleanupCtx, purge, "main tx commit without rotation write") {
				recordSigningKeyshareSecretCleanupOutcome(cleanupCtx, "commit_without_write_deleted_new")
			}
			return nil
		})
	})

	return true, outcome, nil
}

func refsForAll(versions map[uuid.UUID]int32) []signingKeyshareSecretVersionRef {
	refs := make([]signingKeyshareSecretVersionRef, 0, len(versions))
	for id, version := range versions {
		refs = append(refs, signingKeyshareSecretVersionRef{signingKeyshareID: id, version: version})
	}
	return refs
}
