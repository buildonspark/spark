package entephemeral

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"

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
	secret, err := tx.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(signingKeyshareID)).
		Order(signingkeysharesecret.ByVersion(sql.OrderDesc())).
		ForUpdate().
		First(ctx)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

// CreateSigningKeyshareSecretVersion inserts a secret version with the given version number.
// The caller is responsible for choosing a version that does not already exist for this keyshare;
// inserting a duplicate (signingKeyshareID, version) pair will return a constraint error (check
// with IsConstraintError). Version numbers do not need to be sequential, but callers that want
// automatic sequential versioning should use AddSigningKeyshareSecretVersion instead.
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

func lockSigningKeyshareIDForVersioning(ctx context.Context, tx *Tx, signingKeyshareID uuid.UUID) error {
	if tx.config.driver.Dialect() != dialect.Postgres {
		return fmt.Errorf(
			"advisory locking for signing keyshare versioning is only supported on Postgres, got %q",
			tx.config.driver.Dialect(),
		)
	}

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
