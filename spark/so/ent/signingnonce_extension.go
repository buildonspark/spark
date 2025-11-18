package ent

import (
	"context"
	"fmt"
	"slices"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent/signingnonce"
	"github.com/lightsparkdev/spark/so/frost"
)

// GetSigningNonceFromCommitment returns the signing nonce associated with the given commitment.
func GetSigningNonceFromCommitment(ctx context.Context, _ *so.Config, commitment frost.SigningCommitment) (frost.SigningNonce, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return frost.SigningNonce{}, err
	}

	nonce, err := db.SigningNonce.Query().Where(signingnonce.NonceCommitment(commitment)).First(ctx)
	if err != nil {
		return frost.SigningNonce{}, err
	}

	return nonce.Nonce, nil
}

// GetSigningNoncesForUpdate returns the signing nonces associated with the given commitments, and locks them for update.
func GetSigningNoncesForUpdate(ctx context.Context, _ *so.Config, commitments []frost.SigningCommitment) (map[frost.SigningCommitment]*SigningNonce, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	noncesResult, err := db.SigningNonce.Query().Where(signingnonce.NonceCommitmentIn(commitments...)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[frost.SigningCommitment]*SigningNonce, len(noncesResult))
	for _, nonce := range noncesResult {
		result[nonce.NonceCommitment] = nonce
	}
	return result, nil
}

// BulkUpdateRetryFingerprints updates the retry fingerprints for multiple signing nonces in a single query.
func BulkUpdateRetryFingerprints(ctx context.Context, nonces map[frost.SigningCommitment]*SigningNonce, retryFingerprints map[frost.SigningCommitment][]byte) error {
	if len(retryFingerprints) == 0 {
		return nil
	}

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// Collect all updates to batch them and avoid N+1 queries
	builders := make([]*SigningNonceCreate, 0, len(retryFingerprints))
	for commitment, fingerprint := range retryFingerprints {
		nonce, exists := nonces[commitment]
		if !exists {
			return fmt.Errorf("nonce not found for commitment")
		}

		// Build upsert for batch update. Since records always exist (queried above),
		// OnConflict will always UPDATE, never INSERT. We set ID (for matching), required fields, and the fields we want to update.
		builders = append(builders,
			db.SigningNonce.Create().
				SetID(nonce.ID).
				SetNonce(nonce.Nonce).
				SetNonceCommitment(nonce.NonceCommitment).
				SetRetryFingerprint(fingerprint),
		)
	}

	// Execute all updates in batch to avoid N+1 queries.
	// We use CreateBulk with OnConflict as a workaround since Ent doesn't have native bulk UPDATE support.
	// Since all records exist (queried above), OnConflict will always UPDATE, never INSERT.
	// Batch in chunks to avoid PostgreSQL parameter limit (65535).
	const maxBatchSize = 1000
	for chunk := range slices.Chunk(builders, maxBatchSize) {
		err = db.SigningNonce.CreateBulk(chunk...).
			OnConflictColumns(signingnonce.FieldID).
			Update(func(u *SigningNonceUpsert) {
				u.UpdateRetryFingerprint()
			}).
			Exec(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}
