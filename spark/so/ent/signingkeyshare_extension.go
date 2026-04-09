package ent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/lightsparkdev/spark/common/keys"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common/logging"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/entephemeral/predicate"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultMinAvailableKeys is the minimum number of DKG keys that should be available at all times.
// If the number of available keys drops below this threshold, DKG will be triggered to generate new
// keys.
const defaultMinAvailableKeys = 100_000

var (
	ErrSigningKeyshareSecretUnavailable        = errors.New("signing keyshare secret unavailable")
	ErrSigningKeyshareSecretMissing            = errors.New("signing keyshare secret missing")
	signingKeyshareSecretCleanupFailureCounter metric.Int64Counter
	signingKeyshareSecretCleanupCounterInit    sync.Once
)

func getSigningKeyshareSecretCleanupFailureCounter() metric.Int64Counter {
	signingKeyshareSecretCleanupCounterInit.Do(func() {
		meter := otel.GetMeterProvider().Meter("spark.db.ent")
		counter, err := meter.Int64Counter(
			"spark_db_ent_signing_keyshare_secret_cleanup_failures_total",
			metric.WithDescription("Total number of best-effort signing keyshare secret cleanup failures"),
			metric.WithUnit("{count}"),
		)
		if err != nil {
			otel.Handle(err)
			signingKeyshareSecretCleanupFailureCounter = noop.Int64Counter{}
			return
		}
		signingKeyshareSecretCleanupFailureCounter = counter
	})
	return signingKeyshareSecretCleanupFailureCounter
}

func recordSigningKeyshareSecretCleanupFailure(
	ctx context.Context,
	stage string,
	reason string,
) {
	getSigningKeyshareSecretCleanupFailureCounter().Add(
		ctx,
		1,
		metric.WithAttributes(
			attribute.String("stage", stage),
			attribute.String("reason", reason),
		),
	)
}

type signingKeyshareSecretDualWriteDecisionContextKey struct{}

// FreezeSigningKeyshareSecretDualWriteDecision computes the dual-write decision once and stores it on context.
func FreezeSigningKeyshareSecretDualWriteDecision(ctx context.Context) context.Context {
	return context.WithValue(
		ctx,
		signingKeyshareSecretDualWriteDecisionContextKey{},
		shouldDualWriteSigningKeyshareSecret(ctx),
	)
}

func shouldDualWriteSigningKeyshareSecret(ctx context.Context) bool {
	if ctx != nil {
		if decision, ok := ctx.Value(signingKeyshareSecretDualWriteDecisionContextKey{}).(bool); ok {
			return decision
		}
	}

	knobService := knobs.GetKnobsService(ctx)
	return knobService.RolloutRandom(knobs.KnobSoSigningKeyshareDualWriteSecret, 100)
}

func deleteSigningKeyshareSecretVersionBestEffort(ctx context.Context, signingKeyshareID uuid.UUID, version int32, reason string) {
	logger := logging.GetLoggerFromContext(ctx)

	ephemeralTx, err := entephemeral.GetTxFromContext(ctx)
	if err != nil {
		recordSigningKeyshareSecretCleanupFailure(ctx, "get_tx", reason)
		logger.With(zap.Error(err)).Sugar().Warnf(
			"failed to start ephemeral tx to cleanup signing keyshare %s version %d (%s)",
			signingKeyshareID,
			version,
			reason,
		)
		return
	}
	defer func() { _ = ephemeralTx.Rollback() }()

	if err := entephemeral.DeleteSigningKeyshareSecretVersion(ctx, signingKeyshareID, version); err != nil {
		recordSigningKeyshareSecretCleanupFailure(ctx, "delete", reason)
		logger.With(zap.Error(err)).Sugar().Warnf(
			"failed to delete signing keyshare %s version %d (%s)",
			signingKeyshareID,
			version,
			reason,
		)
		return
	}

	if err := ephemeralTx.Commit(); err != nil {
		recordSigningKeyshareSecretCleanupFailure(ctx, "commit", reason)
		logger.With(zap.Error(err)).Sugar().Warnf(
			"failed to commit ephemeral cleanup tx for signing keyshare %s version %d (%s)",
			signingKeyshareID,
			version,
			reason,
		)
	}
}

type signingKeyshareSecretRotation struct {
	newVersion   *int32
	oldVersion   *int32
	useEphemeral bool
}

func prepareSigningKeyshareSecretRotation(ctx context.Context, signingKeyshareID uuid.UUID, newSecretShare keys.Private) (*signingKeyshareSecretRotation, error) {
	ephemeralTx, err := entephemeral.GetTxFromContext(ctx)
	if err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			return &signingKeyshareSecretRotation{
				newVersion:   nil,
				oldVersion:   nil,
				useEphemeral: false,
			}, nil
		}
		return nil, err
	}
	defer func() { _ = ephemeralTx.Rollback() }()

	latest, err := entephemeral.GetLatestSigningKeyshareSecretVersionForUpdate(ctx, signingKeyshareID)
	if err != nil {
		return nil, err
	}

	var oldVersion *int32
	var newVersion int32
	if latest != nil {
		if latest.Version == math.MaxInt32 {
			return nil, fmt.Errorf("signing keyshare secret version overflow for keyshare %s", signingKeyshareID)
		}
		oldVersion = new(int32)
		*oldVersion = latest.Version
		newVersion = latest.Version + 1
	}

	if _, err := entephemeral.CreateSigningKeyshareSecretVersion(ctx, signingKeyshareID, newVersion, newSecretShare); err != nil {
		return nil, err
	}

	if err := ephemeralTx.Commit(); err != nil {
		return nil, err
	}

	// Cleanup must not inherit request cancellation, or rollback/commit hooks can leave orphaned versions.
	cleanupCtx := context.WithoutCancel(ctx)

	tx, err := GetTxFromContext(ctx)
	if err != nil {
		// If we cannot access the main transaction after creating the new secret version,
		// delete the newly-created version to avoid dangling references.
		deleteSigningKeyshareSecretVersionBestEffort(cleanupCtx, signingKeyshareID, newVersion, "main tx unavailable after ephemeral commit")
		return nil, err
	}

	tx.OnRollback(func(fn Rollbacker) Rollbacker {
		return RollbackFunc(func(ctx context.Context, tx *Tx) error {
			// Preserve rollback semantics from the wrapped hook while always attempting
			// ephemeral cleanup to avoid leaking versions when the main tx aborts.
			err := fn.Rollback(ctx, tx)
			deleteSigningKeyshareSecretVersionBestEffort(cleanupCtx, signingKeyshareID, newVersion, "main tx rollback")
			return err
		})
	})

	if oldVersion != nil {
		oldVersionValue := *oldVersion
		tx.OnCommit(func(fn Committer) Committer {
			return CommitFunc(func(ctx context.Context, tx *Tx) error {
				err := fn.Commit(ctx, tx)
				if err == nil {
					deleteSigningKeyshareSecretVersionBestEffort(cleanupCtx, signingKeyshareID, oldVersionValue, "main tx commit")
				}
				return err
			})
		})
	}

	return &signingKeyshareSecretRotation{
		newVersion:   &newVersion,
		oldVersion:   oldVersion,
		useEphemeral: true,
	}, nil
}

// UpdateSigningKeyshareWithRotatedSecret rotates the external secret version for a keyshare and
// updates the signing_keyshares row in the main database within the same request transaction flow.
// Batch callers must freeze the dual-write rollout decision once via
// FreezeSigningKeyshareSecretDualWriteDecision and reuse that context across all invocations.
func UpdateSigningKeyshareWithRotatedSecret(
	ctx context.Context,
	signingKeyshareID uuid.UUID,
	newSecretShare keys.Private,
	mutate func(*SigningKeyshareUpdateOne) *SigningKeyshareUpdateOne,
) (*SigningKeyshare, error) {
	rotation, err := prepareSigningKeyshareSecretRotation(ctx, signingKeyshareID, newSecretShare)
	if err != nil {
		return nil, err
	}

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	update := db.SigningKeyshare.UpdateOneID(signingKeyshareID)
	if rotation.useEphemeral && rotation.newVersion != nil {
		update = update.SetSecretVersion(*rotation.newVersion)
	} else {
		update = update.ClearSecretVersion()
	}
	// prepareSigningKeyshareSecretRotation has already committed the ephemeral write by this point.
	// Batch callers should freeze the dual-write rollout decision up front via
	// FreezeSigningKeyshareSecretDualWriteDecision so this branch stays stable for the whole flow.
	if !rotation.useEphemeral || shouldDualWriteSigningKeyshareSecret(ctx) {
		update = update.SetSecretShare(newSecretShare)
	} else {
		update = update.ClearSecretShare()
	}
	if mutate != nil {
		update = mutate(update)
	}

	return update.Save(ctx)
}

// PrepareSigningKeyshareCreateWithSecret writes a secret version in the ephemeral store and
// mutates the create builder so main-db creation references that version.
func PrepareSigningKeyshareCreateWithSecret(
	ctx context.Context,
	create *SigningKeyshareCreate,
	signingKeyshareID uuid.UUID,
	secretShare keys.Private,
) (*SigningKeyshareCreate, error) {
	rotation, err := prepareSigningKeyshareSecretRotation(ctx, signingKeyshareID, secretShare)
	if err != nil {
		return nil, err
	}

	if rotation.useEphemeral && rotation.newVersion != nil {
		create = create.SetSecretVersion(*rotation.newVersion)
	}
	if !rotation.useEphemeral || shouldDualWriteSigningKeyshareSecret(ctx) {
		create = create.SetSecretShare(secretShare)
	}

	return create, nil
}

// GetSecretShare returns the secret share for this keyshare using the following order:
// 1) signing_keyshares.secret_share, 2) preloaded ExternalSecret, 3) ephemeral secret store lookup by secret_version.
func (sk *SigningKeyshare) GetSecretShare(ctx context.Context) (*keys.Private, error) {
	// SecretShare is immutable after struct initialization (set during DB scan),
	// so it is safe to read without holding secretMu.
	if sk.SecretShare != nil {
		return sk.SecretShare, nil
	}

	// ExternalSecret is mutable cache state on the entity pointer, so accesses must be synchronized.
	sk.secretMu.Lock()
	defer sk.secretMu.Unlock()

	// SecretShare was already checked above (it is immutable), only ExternalSecret
	// needs re-checking under the lock to guard against concurrent fetches.
	if sk.ExternalSecret != nil {
		return sk.ExternalSecret, nil
	}
	if sk.SecretVersion == nil {
		return nil, fmt.Errorf(
			"%w: signing keyshare %s has null secret_share in main DB and no secret_version",
			ErrSigningKeyshareSecretMissing,
			sk.ID,
		)
	}

	logging.GetLoggerFromContext(ctx).Sugar().Infof(
		"signing keyshare %s secret not hydrated; fetching from ephemeral store (version=%d)",
		sk.ID,
		*sk.SecretVersion,
	)

	secret, err := entephemeral.GetSigningKeyshareSecretVersion(ctx, sk.ID, *sk.SecretVersion)
	if err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			return nil, fmt.Errorf(
				"%w: signing keyshare %s secret_share is null in main DB and ephemeral DB is unavailable",
				ErrSigningKeyshareSecretUnavailable,
				sk.ID,
			)
		}
		if errors.Is(err, entephemeral.ErrNoSecretVersion) {
			return nil, fmt.Errorf(
				"%w: signing keyshare %s version %d was not found in ephemeral DB",
				ErrSigningKeyshareSecretMissing,
				sk.ID,
				*sk.SecretVersion,
			)
		}
		return nil, fmt.Errorf("failed to fetch secret for signing keyshare %s version %d: %w", sk.ID, *sk.SecretVersion, err)
	}

	// Store a copy before taking its address so that callers holding the
	// returned pointer cannot corrupt the cache via writes.
	fetched := secret.SecretShare
	sk.ExternalSecret = &fetched
	return sk.ExternalSecret, nil
}

func setExternalSecret(keyshare *SigningKeyshare, secret keys.Private) {
	keyshare.secretMu.Lock()
	defer keyshare.secretMu.Unlock()
	keyshare.ExternalSecret = &secret
}

func hasExternalSecret(keyshare *SigningKeyshare) bool {
	keyshare.secretMu.Lock()
	defer keyshare.secretMu.Unlock()
	return keyshare.ExternalSecret != nil
}

// HydrateSigningKeyshareSecrets preloads external secret shares for keyshares that do not
// have secret_share populated on the main signing_keyshares table.
func HydrateSigningKeyshareSecrets(ctx context.Context, keyshares []*SigningKeyshare) error {
	type signingKeyshareSecretLookupKey struct {
		id      uuid.UUID
		version int32
	}

	keysharesByLookup := make(map[signingKeyshareSecretLookupKey][]*SigningKeyshare)
	secretLookupPredicates := make([]predicate.SigningKeyshareSecret, 0, len(keyshares))
	for _, keyshare := range keyshares {
		if keyshare == nil || keyshare.SecretShare != nil || hasExternalSecret(keyshare) || keyshare.SecretVersion == nil {
			continue
		}
		lookupKey := signingKeyshareSecretLookupKey{id: keyshare.ID, version: *keyshare.SecretVersion}
		if _, exists := keysharesByLookup[lookupKey]; exists {
			keysharesByLookup[lookupKey] = append(keysharesByLookup[lookupKey], keyshare)
			continue
		}
		keysharesByLookup[lookupKey] = []*SigningKeyshare{keyshare}
		secretLookupPredicates = append(secretLookupPredicates, signingkeysharesecret.And(
			signingkeysharesecret.SigningKeyshareIDEQ(keyshare.ID),
			signingkeysharesecret.VersionEQ(*keyshare.SecretVersion),
		))
	}

	if len(secretLookupPredicates) == 0 {
		return nil
	}

	ephemeralDB, err := entephemeral.GetDbFromContext(ctx)
	if err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			return fmt.Errorf(
				"%w: one or more signing keyshares have null secret_share in main DB and ephemeral DB is unavailable",
				ErrSigningKeyshareSecretUnavailable,
			)
		}
		return err
	}

	secrets, err := ephemeralDB.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.Or(secretLookupPredicates...)).
		All(ctx)
	if err != nil {
		return err
	}

	for _, secret := range secrets {
		lookupKey := signingKeyshareSecretLookupKey{id: secret.SigningKeyshareID, version: secret.Version}
		keyshareSet, exists := keysharesByLookup[lookupKey]
		if !exists {
			continue
		}
		for _, keyshare := range keyshareSet {
			setExternalSecret(keyshare, secret.SecretShare)
		}
	}
	missing := make([]string, 0)
	for lookupKey, keyshareSet := range keysharesByLookup {
		allHydrated := true
		for _, keyshare := range keyshareSet {
			if !hasExternalSecret(keyshare) {
				allHydrated = false
				break
			}
		}
		if allHydrated {
			continue
		}
		missing = append(missing, fmt.Sprintf("%s@v%d", lookupKey.id, lookupKey.version))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf(
			"%w: signing keyshares not found in ephemeral store: %s",
			ErrSigningKeyshareSecretMissing,
			strings.Join(missing, ", "),
		)
	}

	return nil
}

// TweakKeyShare tweaks the given keyshare with the given tweak, updates the keyshare in the database and returns the updated keyshare.
func (sk *SigningKeyshare) TweakKeyShare(ctx context.Context, shareTweak keys.Private, pubKeyTweak keys.Public, pubKeySharesTweak map[string]keys.Public) (*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.TweakKeyShare")
	defer span.End()

	if err := HydrateSigningKeyshareSecrets(ctx, []*SigningKeyshare{sk}); err != nil {
		return nil, err
	}

	secretShare, err := sk.GetSecretShare(ctx)
	if err != nil {
		return nil, err
	}

	newSecretShare := secretShare.Add(shareTweak)
	newPubKey := sk.PublicKey.Add(pubKeyTweak)

	newPublicShares := make(map[string]keys.Public)
	for id, pubShare := range sk.PublicShares {
		newPublicShares[id] = pubShare.Add(pubKeySharesTweak[id])
	}

	return UpdateSigningKeyshareWithRotatedSecret(
		ctx,
		sk.ID,
		newSecretShare,
		func(update *SigningKeyshareUpdateOne) *SigningKeyshareUpdateOne {
			return update.
				SetPublicKey(newPubKey).
				SetPublicShares(newPublicShares)
		},
	)
}

// MarshalProto converts a SigningKeyshare to a spark protobuf SigningKeyshare.
func (sk *SigningKeyshare) MarshalProto() *pb.SigningKeyshare {
	var ownerIdentifiers []string
	for identifier := range sk.PublicShares {
		ownerIdentifiers = append(ownerIdentifiers, identifier)
	}

	return &pb.SigningKeyshare{
		OwnerIdentifiers: ownerIdentifiers,
		Threshold:        uint32(sk.MinSigners),
		PublicKey:        sk.PublicKey.Serialize(),
		PublicShares:     keys.ToBytesMap(sk.PublicShares),
		UpdatedTime:      timestamppb.New(sk.UpdateTime),
	}
}

// GetUnusedSigningKeyshares returns the available keyshares for the given coordinator index.
func GetUnusedSigningKeyshares(ctx context.Context, config *so.Config, keyshareCount int) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetUnusedSigningKeyshares")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}

	signingKeyshares, err := getUnusedSigningKeysharesTx(ctx, tx.Client(), config, keyshareCount)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			logger.Error("Failed to rollback transaction", zap.Error(rollbackErr))
		}
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return signingKeyshares, nil
}

// getUnusedSigningKeysharesTx runs inside an existing database client (which may be backed by a transaction).
// Caller is responsible for committing/rolling-back the transaction if needed.
func getUnusedSigningKeysharesTx(ctx context.Context, client *Client, cfg *so.Config, keyshareCount int) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.getUnusedSigningKeysharesTx")
	defer span.End()

	if keyshareCount <= 0 {
		return nil, fmt.Errorf("keyshare count must be greater than 0")
	}

	// Prevent keyshare exhaustion attacks by limiting maximum request size
	maxKeysharesPerRequest := int(knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoMaxKeysharesPerRequest, 1000))
	if keyshareCount > maxKeysharesPerRequest {
		return nil, fmt.Errorf("keyshare request too large: requested %d, maximum allowed %d", keyshareCount, maxKeysharesPerRequest)
	}

	//nolint:forbidigo // We have to use this API to set these parameters, which is needed to optimize the performance of the query below.
	_, err := client.ExecContext(ctx, `
		SET LOCAL seq_page_cost = 10.0;
		SET LOCAL random_page_cost = 1.0;
	`)
	if err != nil {
		return nil, err
	}

	var updatedKeyshares []*SigningKeyshare

	//nolint:forbidigo // We use a custom a custom query here to select and update the keyshares in a single query, while skipping locked rows to avoid contention.
	rows, err := client.QueryContext(ctx, `
		WITH selected_ids AS (
			SELECT id FROM signing_keyshares
			WHERE status = 'AVAILABLE' AND coordinator_index = $1
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE signing_keyshares
		SET status = 'IN_USE', update_time = NOW()
		FROM selected_ids
		WHERE signing_keyshares.id = selected_ids.id
		RETURNING
			signing_keyshares.id,
			signing_keyshares.create_time,
			signing_keyshares.update_time,
			signing_keyshares.status,
			signing_keyshares.secret_share,
			signing_keyshares.secret_version,
			signing_keyshares.public_shares,
			signing_keyshares.public_key,
			signing_keyshares.min_signers,
			signing_keyshares.coordinator_index
	`, []any{cfg.Index, keyshareCount}...)
	if err != nil {
		return nil, err
	}
	MarkTxDirty(ctx)
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			// If ScanSlice already returned an error, we don't want to overwrite it,
			// so just log the close error.
			logging.GetLoggerFromContext(ctx).Error("failed to close rows", zap.Error(cerr))
			span.RecordError(cerr)
		}
	}()

	if err := sql.ScanSlice(rows, &updatedKeyshares); err != nil {
		return nil, err
	}

	if len(updatedKeyshares) < keyshareCount {
		return nil, fmt.Errorf("not enough signing keyshares available (needed %d, got %d)", keyshareCount, len(updatedKeyshares))
	}

	return updatedKeyshares, nil
}

// MarkSigningKeysharesAsUsed marks the given keyshares as used. If any of the keyshares are not
// found or not available, it returns an error.
func MarkSigningKeysharesAsUsed(ctx context.Context, _ *so.Config, ids []uuid.UUID) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.MarkSigningKeysharesAsUsed")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	logger.Sugar().Infof("Marking %d keyshares as used", len(ids))

	var updatedKeyshares []*SigningKeyshare

	//nolint:forbidigo // We use a custom a custom query here to select and update the keyshares in a single query
	rows, err := db.QueryContext(ctx, `
		UPDATE signing_keyshares
		SET status = 'IN_USE', update_time = NOW()
		WHERE signing_keyshares.status = 'AVAILABLE'
		AND signing_keyshares.id = ANY($1)
		RETURNING
			signing_keyshares.id,
			signing_keyshares.create_time,
			signing_keyshares.update_time,
			signing_keyshares.status,
			signing_keyshares.secret_share,
			signing_keyshares.secret_version,
			signing_keyshares.public_shares,
			signing_keyshares.public_key,
			signing_keyshares.min_signers,
			signing_keyshares.coordinator_index
	`, []any{pq.Array(ids)}...)
	if err != nil {
		return nil, err
	}
	MarkTxDirty(ctx)
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			// If ScanSlice already returned an error, we don't want to overwrite it,
			// so just log the close error.
			logging.GetLoggerFromContext(ctx).Error("failed to close rows", zap.Error(cerr))
			span.RecordError(cerr)
		}
	}()

	if err := sql.ScanSlice(rows, &updatedKeyshares); err != nil {
		return nil, err
	}

	if len(updatedKeyshares) != len(ids) {
		missing := make([]uuid.UUID, 0, len(ids)-len(updatedKeyshares))
		updatedSet := make(map[uuid.UUID]struct{}, len(updatedKeyshares))
		for _, k := range updatedKeyshares {
			updatedSet[k.ID] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := updatedSet[id]; !ok {
				missing = append(missing, id)
			}
		}
		return nil, fmt.Errorf("keyshares are not all available: ids=%v (total=%d) could not be reserved from %v", missing, len(ids)-len(updatedKeyshares), ids)
	}

	return updatedKeyshares, nil
}

// GetKeyPackage returns the key package for the given keyshare ID.
func GetKeyPackage(ctx context.Context, config *so.Config, keyshareID uuid.UUID) (*pbfrost.KeyPackage, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetKeyPackage")
	defer span.End()

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	keyshare, err := db.SigningKeyshare.Get(ctx, keyshareID)
	if err != nil {
		return nil, err
	}
	if err := HydrateSigningKeyshareSecrets(ctx, []*SigningKeyshare{keyshare}); err != nil {
		return nil, err
	}
	secretShare, err := keyshare.GetSecretShare(ctx)
	if err != nil {
		return nil, err
	}

	keyPackage := &pbfrost.KeyPackage{
		Identifier:   config.Identifier,
		SecretShare:  secretShare.Serialize(),
		PublicShares: keys.ToBytesMap(keyshare.PublicShares),
		PublicKey:    keyshare.PublicKey.Serialize(),
		MinSigners:   uint32(keyshare.MinSigners),
	}

	return keyPackage, nil
}

// GetKeyPackages returns the key packages for the given keyshare IDs.
func GetKeyPackages(ctx context.Context, config *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetKeyPackages")
	defer span.End()

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	keyshares, err := db.SigningKeyshare.Query().Where(
		signingkeyshare.IDIn(keyshareIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if err := HydrateSigningKeyshareSecrets(ctx, keyshares); err != nil {
		return nil, err
	}

	keyPackages := make(map[uuid.UUID]*pbfrost.KeyPackage, len(keyshares))
	for _, keyshare := range keyshares {
		secretShare, secretErr := keyshare.GetSecretShare(ctx)
		if secretErr != nil {
			return nil, secretErr
		}

		keyPackages[keyshare.ID] = &pbfrost.KeyPackage{
			Identifier:   config.Identifier,
			SecretShare:  secretShare.Serialize(),
			PublicShares: keys.ToBytesMap(keyshare.PublicShares),
			PublicKey:    keyshare.PublicKey.Serialize(),
			MinSigners:   uint32(keyshare.MinSigners),
		}
	}

	return keyPackages, nil
}

// GetKeyPackagesArray returns the keyshares for the given keyshare IDs.
// The order of the keyshares in the result is the same as the order of the keyshare IDs.
func GetKeyPackagesArray(ctx context.Context, keyshareIDs []uuid.UUID) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetKeyPackagesArray")
	defer span.End()

	keysharesMap, err := GetSigningKeysharesMapWithSecrets(ctx, keyshareIDs)
	if err != nil {
		return nil, err
	}

	result := make([]*SigningKeyshare, len(keyshareIDs))
	for i, id := range keyshareIDs {
		result[i] = keysharesMap[id]
	}

	return result, nil
}

// GetSigningKeysharesMap returns the keyshares for the given keyshare IDs.
// The order of the keyshares in the result is the same as the order of the keyshare IDs.
func GetSigningKeysharesMap(ctx context.Context, keyshareIDs []uuid.UUID) (map[uuid.UUID]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetSigningKeysharesMap")
	defer span.End()

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(err)
	}

	keyshares, err := db.SigningKeyshare.Query().
		Modify(func(s *sql.Selector) {
			s.Where(sql.P(func(b *sql.Builder) {
				b.Ident(signingkeyshare.FieldID).
					WriteString(" = ANY(").
					Arg(pq.Array(keyshareIDs)).
					WriteByte(')')
			}))
		}).
		All(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(err)
	}

	keysharesMap := make(map[uuid.UUID]*SigningKeyshare, len(keyshares))
	for _, keyshare := range keyshares {
		keysharesMap[keyshare.ID] = keyshare
	}

	return keysharesMap, nil
}

// GetSigningKeysharesMapWithSecrets returns keyshares with external secrets preloaded when available.
func GetSigningKeysharesMapWithSecrets(ctx context.Context, keyshareIDs []uuid.UUID) (map[uuid.UUID]*SigningKeyshare, error) {
	keysharesMap, err := GetSigningKeysharesMap(ctx, keyshareIDs)
	if err != nil {
		return nil, err
	}

	keyshares := make([]*SigningKeyshare, 0, len(keysharesMap))
	for _, keyshare := range keysharesMap {
		keyshares = append(keyshares, keyshare)
	}
	if err := HydrateSigningKeyshareSecrets(ctx, keyshares); err != nil {
		return nil, err
	}

	return keysharesMap, nil
}

// sumOfSigningKeyshares returns an aggregate keyshare with only the fields needed by its callers:
// ID, SecretShare, PublicKey, and PublicShares. SecretVersion is always nil on the result
// (the summed secret is stored directly in SecretShare regardless of input versions).
// All other SigningKeyshare fields are left unset and must not be accessed by callers.
//
// If the ephemeral DB is unavailable, GetSecretShare will return ErrSigningKeyshareSecretUnavailable,
// which propagates to callers (CalculateAndStoreLastKey, AggregateKeyshares) as a hard failure.
// Callers should treat this error as a transient condition and retry.
func sumOfSigningKeyshares(ctx context.Context, keyshares []*SigningKeyshare) (*SigningKeyshare, error) {
	if len(keyshares) == 0 {
		return nil, fmt.Errorf("at least one keyshare is required")
	}
	firstSecret, err := keyshares[0].GetSecretShare(ctx)
	if err != nil {
		return nil, err
	}

	sumSecret := *firstSecret
	sum := &SigningKeyshare{
		ID:           keyshares[0].ID,
		PublicKey:    keyshares[0].PublicKey,
		SecretShare:  &sumSecret,
		PublicShares: make(map[string]keys.Public, len(keyshares[0].PublicShares)),
	}
	sum.SecretVersion = nil
	maps.Copy(sum.PublicShares, keyshares[0].PublicShares)

	for _, keyshare := range keyshares[1:] {
		keyshareSecret, secretErr := keyshare.GetSecretShare(ctx)
		if secretErr != nil {
			return nil, secretErr
		}
		newSecret := sum.SecretShare.Add(*keyshareSecret)
		sum.SecretShare = &newSecret
		sum.PublicKey = sum.PublicKey.Add(keyshare.PublicKey)

		for shareID, publicShare := range sum.PublicShares {
			sum.PublicShares[shareID] = publicShare.Add(keyshare.PublicShares[shareID])
		}
	}
	return sum, nil
}

// CalculateAndStoreLastKey calculates the last key from the given keyshares and stores it in the database.
// The target = sum(keyshares) + last_key
func CalculateAndStoreLastKey(ctx context.Context, _ *so.Config, target *SigningKeyshare, keyshares []*SigningKeyshare, id uuid.UUID) (*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.CalculateAndStoreLastKey")
	defer span.End()

	if len(keyshares) == 0 {
		return target, nil
	}
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Calculating last key for %d keyshares", len(keyshares))

	keysharesToHydrate := make([]*SigningKeyshare, 0, len(keyshares)+1)
	keysharesToHydrate = append(keysharesToHydrate, keyshares...)
	keysharesToHydrate = append(keysharesToHydrate, target)
	if err := HydrateSigningKeyshareSecrets(ctx, keysharesToHydrate); err != nil {
		return nil, err
	}

	sumKeyshare, err := sumOfSigningKeyshares(ctx, keyshares)
	if err != nil {
		return nil, fmt.Errorf("failed to sum keyshares: %w", err)
	}
	targetSecretShare, err := target.GetSecretShare(ctx)
	if err != nil {
		return nil, err
	}

	lastSecretShare := targetSecretShare.Sub(*sumKeyshare.SecretShare)
	verifyLastKey := sumKeyshare.SecretShare.Add(lastSecretShare)

	if !verifyLastKey.Equals(*targetSecretShare) {
		return nil, fmt.Errorf("last key verification failed")
	}

	verifyingKey := target.PublicKey.Sub(sumKeyshare.PublicKey)
	verifyVerifyingKey := keyshares[0].PublicKey.Add(verifyingKey)

	if !verifyVerifyingKey.Equals(target.PublicKey) {
		return nil, fmt.Errorf("verifying key verification failed")
	}

	publicShares := make(map[string]keys.Public)
	for i, publicShare := range target.PublicShares {
		publicShares[i] = publicShare.Sub(sumKeyshare.PublicShares[i])
	}

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	ctx = FreezeSigningKeyshareSecretDualWriteDecision(ctx)
	lastKeyCreate, err := PrepareSigningKeyshareCreateWithSecret(
		ctx,
		db.SigningKeyshare.Create().
			SetID(id).
			SetPublicShares(publicShares).
			SetPublicKey(verifyingKey).
			SetStatus(st.KeyshareStatusInUse).
			SetCoordinatorIndex(0).
			SetMinSigners(target.MinSigners),
		id,
		lastSecretShare,
	)
	if err != nil {
		return nil, err
	}

	lastKey, err := lastKeyCreate.Save(ctx)
	if err != nil {
		return nil, err
	}

	return lastKey, nil
}

// AggregateKeyshares aggregates the given keyshares and updates the keyshare in the database.
func AggregateKeyshares(ctx context.Context, _ *so.Config, keyshares []*SigningKeyshare, updateKeyshareID uuid.UUID) (*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.AggregateKeyshares")
	defer span.End()

	if err := HydrateSigningKeyshareSecrets(ctx, keyshares); err != nil {
		return nil, err
	}

	sumKeyshare, err := sumOfSigningKeyshares(ctx, keyshares)
	if err != nil {
		return nil, fmt.Errorf("failed to sum keyshares: %w", err)
	}

	updateKeyshare, err := UpdateSigningKeyshareWithRotatedSecret(
		ctx,
		updateKeyshareID,
		*sumKeyshare.SecretShare,
		func(update *SigningKeyshareUpdateOne) *SigningKeyshareUpdateOne {
			return update.
				SetPublicKey(sumKeyshare.PublicKey).
				SetPublicShares(sumKeyshare.PublicShares)
		},
	)
	if err != nil {
		return nil, err
	}

	return updateKeyshare, nil
}

// RunDKGIfNeeded checks if the keyshare count is below the threshold and runs DKG if needed.
func RunDKGIfNeeded(ctx context.Context, config *so.Config) error {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.RunDKGIfNeeded")
	defer span.End()

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	minAvailableKeys := defaultMinAvailableKeys
	if config.DKGConfig.MinAvailableKeys != nil && *config.DKGConfig.MinAvailableKeys > 0 {
		minAvailableKeys = *config.DKGConfig.MinAvailableKeys
	}

	// Use optimized query that stops scanning after finding minAvailableKeys+1 rows
	const query = `
		SELECT COUNT(*) > $1 AS over_minimum
		FROM (
			SELECT 1
			FROM signing_keyshares
			WHERE status = $2 AND coordinator_index = $3
			LIMIT $4
		) AS limited
	`

	//nolint:forbidigo // This query runs every 10 seconds, scans a lot of rows, and can't be expressed using the ent query builder.
	rows, err := db.QueryContext(ctx, query, minAvailableKeys, string(st.KeyshareStatusAvailable), config.Index, minAvailableKeys+1)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			logging.GetLoggerFromContext(ctx).Error("failed to close rows", zap.Error(cerr))
			span.RecordError(cerr)
		}
	}()

	var overMinimumAvailableKeys bool
	if rows.Next() {
		if err := rows.Scan(&overMinimumAvailableKeys); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if overMinimumAvailableKeys {
		return nil
	}
	return RunDKG(ctx, config)
}

func RunDKG(ctx context.Context, config *so.Config) error {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.RunDKG")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	connection, err := config.SigningOperatorMap[config.Identifier].NewOperatorGRPCConnectionForDKG()
	if err != nil {
		logger.Error("Failed to create connection to DKG coordinator", zap.Error(err))
		return err
	}
	defer connection.Close()
	client := pbdkg.NewDKGServiceClient(connection)

	count := int32(knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoDkgBatchSize, spark.DKGKeyCount))

	_, err = client.StartDkg(ctx, &pbdkg.StartDkgRequest{
		Count: count,
	})
	if err != nil {
		logger.Error("Failed to start DKG", zap.Error(err))
		return err
	}

	return nil
}
