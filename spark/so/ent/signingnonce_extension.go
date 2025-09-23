package ent

import (
	"context"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent/signingnonce"
	"github.com/lightsparkdev/spark/so/objects"
)

// GetSigningNonceFromCommitment returns the signing nonce associated with the given commitment.
func GetSigningNonceFromCommitment(ctx context.Context, _ *so.Config, commitment objects.SigningCommitment) (*objects.SigningNonce, error) {
	commitmentBytes := commitment.MarshalBinary()

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	nonce, err := db.SigningNonce.Query().Where(signingnonce.NonceCommitment(commitmentBytes)).First(ctx)
	if err != nil {
		return nil, err
	}

	signingNonce := objects.SigningNonce{}
	err = signingNonce.UnmarshalBinary(nonce.Nonce)
	if err != nil {
		return nil, err
	}

	return &signingNonce, nil
}

// GetSigningNoncesForUpdate returns the signing nonces associated with the given commitments, and locks them for update.
func GetSigningNoncesForUpdate(ctx context.Context, _ *so.Config, commitments []objects.SigningCommitment) (map[[66]byte]*SigningNonce, error) {
	commitmentBytes := make([][]byte, len(commitments))
	for i, commitment := range commitments {
		commitmentBytes[i] = commitment.MarshalBinary()
	}
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	noncesResult, err := db.SigningNonce.Query().Where(signingnonce.NonceCommitmentIn(commitmentBytes...)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[[66]byte]*SigningNonce)
	for _, nonce := range noncesResult {
		result[[66]byte(nonce.NonceCommitment)] = nonce
	}
	return result, nil
}
