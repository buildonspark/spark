package entephemeral

import (
	"cmp"
	"context"
	dbSql "database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strings"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
)

var (
	ErrNoSecretVersion = errors.New("no secret version found for signing keyshare")
)

func GetSigningKeyshareSecretVersion(
	ctx context.Context,
	signingKeyshareID uuid.UUID,
	version int32,
) (*SigningKeyshareSecret, error) {
	// This is a pure read path, so use the context client instead of forcing
	// GetTxFromContext. Read-only ephemeral sessions intentionally do not expose
	// explicit transactions.
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	secret, err := db.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(signingKeyshareID), signingkeysharesecret.VersionEQ(version)).
		Only(ctx)
	if err != nil {
		if IsNotFound(err) {
			return nil, ErrNoSecretVersion
		}
		return nil, err
	}
	return secret, nil
}

// GetLatestSigningKeyshareSecretVersionForUpdate returns the latest secret version row and locks it for update.
// Returns (nil, nil) if no version exists yet for this keyshare.
func GetLatestSigningKeyshareSecretVersionForUpdate(
	ctx context.Context,
	signingKeyshareID uuid.UUID,
) (*SigningKeyshareSecret, error) {
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return getLatestSigningKeyshareSecretVersionForUpdateLocked(ctx, tx, signingKeyshareID)
}

func getLatestSigningKeyshareSecretVersionForUpdateLocked(
	ctx context.Context,
	tx *Tx,
	signingKeyshareID uuid.UUID,
) (*SigningKeyshareSecret, error) {
	if err := lockSigningKeyshareIDForVersioning(ctx, tx, signingKeyshareID); err != nil {
		return nil, err
	}

	// NOTE: combining ORDER BY + LIMIT 1 + FOR UPDATE can produce phantom reads in
	// some Postgres plan shapes. This is safe here because the advisory lock acquired
	// above serialises all callers for the same signingKeyshareID, so concurrent
	// writers cannot interleave and produce a different "latest" row between the
	// snapshot and the lock.
	query := tx.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(signingKeyshareID)).
		Order(signingkeysharesecret.ByVersion(sql.OrderDesc()))
	if tx.config.driver.Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}

	secret, err := query.First(ctx)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

// GetLatestSigningKeyshareSecretVersionsForUpdate is the batch form of
// GetLatestSigningKeyshareSecretVersionForUpdate: it acquires every
// keyshare's advisory lock in one statement (in sorted key order, so
// overlapping batch callers cannot deadlock), then locks and returns the
// latest secret version row per keyshare from a single query. Keyshares with
// no version yet are absent from the returned map.
func GetLatestSigningKeyshareSecretVersionsForUpdate(
	ctx context.Context,
	signingKeyshareIDs []uuid.UUID,
) (map[uuid.UUID]*SigningKeyshareSecret, error) {
	if len(signingKeyshareIDs) == 0 {
		return map[uuid.UUID]*SigningKeyshareSecret{}, nil
	}
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := lockSigningKeyshareIDsForVersioning(ctx, tx, signingKeyshareIDs); err != nil {
		return nil, err
	}

	// One statement reads and locks atomically — a read-then-lock two-step
	// would let the orphan purge (which takes no advisory locks) delete a
	// selected row in between and silently drop that keyshare from the
	// result. Locking every version row of these keyshares is slightly wider
	// than the single-row variant's latest-only lock, but a keyshare holds
	// only its current version plus rare short-lived orphans, and callers'
	// batches never overlap (keyshares are transfer-scoped).
	query := tx.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDIn(signingKeyshareIDs...))
	if tx.config.driver.Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	rows, err := query.All(ctx)
	if err != nil {
		return nil, err
	}
	latest := make(map[uuid.UUID]*SigningKeyshareSecret, len(signingKeyshareIDs))
	for _, row := range rows {
		if current, ok := latest[row.SigningKeyshareID]; !ok || row.Version > current.Version {
			latest[row.SigningKeyshareID] = row
		}
	}
	return latest, nil
}

// SigningKeyshareSecretVersionInput describes one row for
// CreateSigningKeyshareSecretVersionsLocked.
type SigningKeyshareSecretVersionInput struct {
	SigningKeyshareID uuid.UUID
	Version           int32
	SecretShare       keys.Private
}

// CreateSigningKeyshareSecretVersionsLocked inserts one secret version row
// per input in a single bulk INSERT. Callers MUST already hold the advisory
// locks for every referenced keyshare (e.g. via
// GetLatestSigningKeyshareSecretVersionsForUpdate) — this function does not
// acquire them. A duplicate (signingKeyshareID, version) pair returns a
// constraint error.
func CreateSigningKeyshareSecretVersionsLocked(
	ctx context.Context,
	items []SigningKeyshareSecretVersionInput,
) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return err
	}
	builders := make([]*SigningKeyshareSecretCreate, len(items))
	for i, item := range items {
		builders[i] = tx.SigningKeyshareSecret.Create().
			SetSigningKeyshareID(item.SigningKeyshareID).
			SetVersion(item.Version).
			SetSecretShare(item.SecretShare)
	}
	return tx.SigningKeyshareSecret.CreateBulk(builders...).Exec(ctx)
}

// CreateSigningKeyshareSecretVersion inserts a secret version with the given version number.
// The caller is responsible for choosing a version that does not already exist for this keyshare;
// inserting a duplicate (signingKeyshareID, version) pair will return a constraint error (check
// with IsConstraintError). Version numbers do not need to be sequential, but callers that want
// automatic sequential versioning should use AddSigningKeyshareSecretVersion instead.
//
// On Postgres, a constraint error from this call aborts the surrounding transaction and any
// subsequent statement on it will fail with SQLSTATE 25P02 until rollback. Callers that need
// idempotent insert-or-read semantics should use GetOrCreateSigningKeyshareSecretVersion instead.
func CreateSigningKeyshareSecretVersion(
	ctx context.Context,
	signingKeyshareID uuid.UUID,
	version int32,
	secretShare keys.Private,
) (*SigningKeyshareSecret, error) {
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := lockSigningKeyshareIDForVersioning(ctx, tx, signingKeyshareID); err != nil {
		return nil, err
	}

	return createSigningKeyshareSecretVersionLocked(ctx, tx, signingKeyshareID, version, secretShare)
}

// GetOrCreateSigningKeyshareSecretVersion atomically inserts or reads the secret version row
// for (signingKeyshareID, version). On conflict the existing row is left untouched (INSERT ...
// ON CONFLICT DO NOTHING), so this never aborts the surrounding Postgres transaction.
//
// The returned row reflects whatever is currently stored in the database. When a row already
// existed, its secret_share may differ from the secretShare argument — callers MUST check the
// returned secret_share against their expectation before treating the call as a no-op.
func GetOrCreateSigningKeyshareSecretVersion(
	ctx context.Context,
	signingKeyshareID uuid.UUID,
	version int32,
	secretShare keys.Private,
) (*SigningKeyshareSecret, error) {
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := lockSigningKeyshareIDForVersioning(ctx, tx, signingKeyshareID); err != nil {
		return nil, err
	}

	// Ent's DoNothing().Exec emits INSERT ... ON CONFLICT DO NOTHING RETURNING id; on conflict
	// no row is returned and the driver surfaces sql.ErrNoRows. That's the no-op success path —
	// only non-ErrNoRows errors are real failures.
	if err := tx.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(signingKeyshareID).
		SetVersion(version).
		SetSecretShare(secretShare).
		OnConflictColumns(signingkeysharesecret.FieldSigningKeyshareID, signingkeysharesecret.FieldVersion).
		DoNothing().
		Exec(ctx); err != nil && !errors.Is(err, dbSql.ErrNoRows) {
		return nil, fmt.Errorf("failed to upsert signing keyshare secret version %d for keyshare %s: %w", version, signingKeyshareID, err)
	}

	secret, err := tx.SigningKeyshareSecret.Query().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(signingKeyshareID),
			signingkeysharesecret.VersionEQ(version),
		).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read back signing keyshare secret version %d for keyshare %s: %w", version, signingKeyshareID, err)
	}
	return secret, nil
}

// CreateSigningKeyshareSecretVersionsBulk inserts one secret version per (signingKeyshareID,
// secretShare) pair at the given version in a single bulk INSERT. Unlike
// CreateSigningKeyshareSecretVersion it does NOT acquire per-keyshare advisory locks and does NOT
// look up an existing version, so callers MUST only use it for freshly created keyshare IDs that
// cannot already have a secret version (e.g. DKG output). A duplicate (signingKeyshareID, version)
// pair returns a constraint error (check with IsConstraintError). signingKeyshareIDs and
// secretShares must be the same length and aligned by index.
func CreateSigningKeyshareSecretVersionsBulk(
	ctx context.Context,
	signingKeyshareIDs []uuid.UUID,
	version int32,
	secretShares []keys.Private,
) error {
	if len(signingKeyshareIDs) != len(secretShares) {
		return fmt.Errorf("mismatched lengths: ids=%d secrets=%d", len(signingKeyshareIDs), len(secretShares))
	}
	if len(signingKeyshareIDs) == 0 {
		return nil
	}

	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return err
	}

	builders := make([]*SigningKeyshareSecretCreate, len(signingKeyshareIDs))
	for i, id := range signingKeyshareIDs {
		builders[i] = tx.SigningKeyshareSecret.Create().
			SetSigningKeyshareID(id).
			SetVersion(version).
			SetSecretShare(secretShares[i])
	}
	return tx.SigningKeyshareSecret.CreateBulk(builders...).Exec(ctx)
}

func AddSigningKeyshareSecretVersion(
	ctx context.Context,
	signingKeyshareID uuid.UUID,
	secretShare keys.Private,
) (*SigningKeyshareSecret, error) {
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := getLatestSigningKeyshareSecretVersionForUpdateLocked(ctx, tx, signingKeyshareID)
	if err != nil {
		return nil, err
	}

	version, err := nextVersion(latest)
	if err != nil {
		return nil, fmt.Errorf("signing keyshare secret version overflow for keyshare %s: %w", signingKeyshareID, err)
	}

	return createSigningKeyshareSecretVersionLocked(ctx, tx, signingKeyshareID, version, secretShare)
}

func nextVersion(latest *SigningKeyshareSecret) (int32, error) {
	if latest == nil {
		return 0, nil
	}
	if latest.Version == math.MaxInt32 {
		return 0, fmt.Errorf("version overflow")
	}
	return latest.Version + 1, nil
}

// createSigningKeyshareSecretVersionLocked inserts a new secret version row assuming
// the advisory lock for signingKeyshareID is already held by the transaction.
func createSigningKeyshareSecretVersionLocked(
	ctx context.Context,
	tx *Tx,
	signingKeyshareID uuid.UUID,
	version int32,
	secretShare keys.Private,
) (*SigningKeyshareSecret, error) {
	return tx.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(signingKeyshareID).
		SetVersion(version).
		SetSecretShare(secretShare).
		Save(ctx)
}

func DeleteSigningKeyshareSecretVersion(
	ctx context.Context,
	signingKeyshareID uuid.UUID,
	version int32,
) error {
	tx, err := GetTxFromContext(ctx)
	if err != nil {
		return err
	}

	affected, err := tx.SigningKeyshareSecret.Delete().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(signingKeyshareID),
			signingkeysharesecret.VersionEQ(version),
		).
		Exec(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNoSecretVersion
	}
	return nil
}

// lockSigningKeyshareIDsForVersioning acquires the advisory locks for all
// given keyshares in one statement. Locks are taken in sorted key order so
// two batch callers with overlapping keyshare sets always acquire them in the
// same sequence and cannot deadlock (pg_advisory_xact_lock is volatile, which
// pins the row-by-row evaluation order of the VALUES scan).
func lockSigningKeyshareIDsForVersioning(ctx context.Context, tx *Tx, signingKeyshareIDs []uuid.UUID) error {
	switch tx.config.driver.Dialect() {
	case dialect.Postgres:
	case dialect.SQLite:
		// See lockSigningKeyshareIDForVersioning: SQLite serializes writes.
		return nil
	default:
		return fmt.Errorf(
			"advisory locking for signing keyshare versioning is only supported on Postgres/SQLite, got %q",
			tx.config.driver.Dialect(),
		)
	}

	lockKeys := make([][2]int32, 0, len(signingKeyshareIDs))
	seen := make(map[[2]int32]struct{}, len(signingKeyshareIDs))
	for _, id := range signingKeyshareIDs {
		hi, lo := signingKeyshareIDToAdvisoryLockKey(id)
		key := [2]int32{hi, lo}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		lockKeys = append(lockKeys, key)
	}
	slices.SortFunc(lockKeys, func(a, b [2]int32) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return cmp.Compare(a[1], b[1])
	})

	txDriver, ok := tx.config.driver.(*txDriver)
	if !ok {
		return fmt.Errorf("unexpected tx driver type: %T", tx.config.driver)
	}

	// The ORDER BY subquery makes the acquisition order part of the query's
	// contract — a bare VALUES scan has no guaranteed output order. The Go-side
	// sort keeps the literal deterministic; the ORDER BY enforces it.
	var sb strings.Builder
	args := make([]any, 0, len(lockKeys)*2)
	sb.WriteString("SELECT pg_advisory_xact_lock(v.hi, v.lo) FROM (SELECT hi, lo FROM (VALUES ")
	for i, key := range lockKeys {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "($%d::int4, $%d::int4)", len(args)+1, len(args)+2)
		args = append(args, key[0], key[1])
	}
	sb.WriteString(") AS x(hi, lo) ORDER BY hi, lo) AS v")
	if _, err := txDriver.ExecContext(ctx, sb.String(), args...); err != nil {
		return err
	}
	return nil
}

func lockSigningKeyshareIDForVersioning(ctx context.Context, tx *Tx, signingKeyshareID uuid.UUID) error {
	switch tx.config.driver.Dialect() {
	case dialect.Postgres:
		// Use transaction-scoped advisory locks in Postgres so concurrent updates on the
		// same keyshare serialize cleanly.
	case dialect.SQLite:
		// Unit tests run entephemeral on SQLite. SQLite does not support advisory locks,
		// and writes are already serialized at the database level; allow the transaction
		// to proceed without an explicit lock.
		return nil
	default:
		return fmt.Errorf(
			"advisory locking for signing keyshare versioning is only supported on Postgres/SQLite, got %q",
			tx.config.driver.Dialect(),
		)
	}

	// Postgres intentionally falls through from the switch above; acquire the
	// transaction-scoped advisory lock here.
	txDriver, ok := tx.config.driver.(*txDriver)
	if !ok {
		return fmt.Errorf("unexpected tx driver type: %T", tx.config.driver)
	}

	lockHi, lockLo := signingKeyshareIDToAdvisoryLockKey(signingKeyshareID)
	if _, err := txDriver.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1::int4, $2::int4)", lockHi, lockLo); err != nil {
		return err
	}
	return nil
}

func signingKeyshareIDToAdvisoryLockKey(signingKeyshareID uuid.UUID) (int32, int32) {
	// Hash the full UUID before splitting into two int32 values to avoid false
	// contention from simple XOR folding collisions when mapping IDs to
	// pg_advisory_xact_lock's (classid, objid) key space.
	hash := fnv.New64a()
	_, _ = hash.Write(signingKeyshareID[:])
	value := hash.Sum64()
	return int32(value >> 32), int32(value)
}
