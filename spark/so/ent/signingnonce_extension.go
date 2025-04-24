package ent

import (
	"context"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent/signingnonce"
	"github.com/lightsparkdev/spark-go/so/objects"
)

// StoreSigningNonce stores the given signing nonce and commitment in the database.
func StoreSigningNonce(ctx context.Context, _ *so.Config, nonce objects.SigningNonce, commitment objects.SigningCommitment) error {
	nonceBytes, err := nonce.MarshalBinary()
	if err != nil {
		return err
	}
	commitmentBytes := commitment.MarshalBinary()

	_, err = GetDbFromContext(ctx).SigningNonce.Create().
		SetNonce(nonceBytes).
		SetNonceCommitment(commitmentBytes).
		Save(ctx)
	return err
}

// GetSigningNonceFromCommitment returns the signing nonce associated with the given commitment.
func GetSigningNonceFromCommitment(ctx context.Context, _ *so.Config, commitment objects.SigningCommitment) (*objects.SigningNonce, error) {
	commitmentBytes := commitment.MarshalBinary()

	nonce, err := GetDbFromContext(ctx).SigningNonce.Query().Where(signingnonce.NonceCommitment(commitmentBytes)).First(ctx)
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

// GetSigningNonces returns the signing nonces associated with the given commitments.
func GetSigningNonces(ctx context.Context, _ *so.Config, commitments []objects.SigningCommitment) (map[[66]byte]*SigningNonce, error) {
	commitmentBytes := make([][]byte, len(commitments))
	for i, commitment := range commitments {
		commitmentBytes[i] = commitment.MarshalBinary()
	}
	noncesResult, err := GetDbFromContext(ctx).SigningNonce.Query().Where(signingnonce.NonceCommitmentIn(commitmentBytes...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[[66]byte]*SigningNonce)
	for _, nonce := range noncesResult {
		result[[66]byte(nonce.NonceCommitment)] = nonce
	}
	return result, nil
}
