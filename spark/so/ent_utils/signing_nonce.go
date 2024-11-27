package ent_utils

import (
	"context"

	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent/signingnonce"
	"github.com/lightsparkdev/spark-go/so/objects"
)

func StoreSigningNonce(ctx context.Context, config *so.Config, nonce objects.SigningNonce, commitment objects.SigningCommitment) error {
	nonceBytes, err := nonce.MarshalBinary()
	if err != nil {
		return err
	}
	commitmentBytes := commitment.MarshalBinary()

	_, err = common.GetDbFromContext(ctx).SigningNonce.Create().
		SetNonce(nonceBytes).
		SetNonceCommitment(commitmentBytes).
		Save(ctx)
	return err
}

func GetSigningNonceFromCommitment(ctx context.Context, config *so.Config, commitment objects.SigningCommitment) (*objects.SigningNonce, error) {
	commitmentBytes := commitment.MarshalBinary()

	nonce, err := common.GetDbFromContext(ctx).SigningNonce.Query().Where(signingnonce.NonceCommitment(commitmentBytes)).First(ctx)
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

func GetSigningNonces(ctx context.Context, config *so.Config, commitments []objects.SigningCommitment) (map[[66]byte]*objects.SigningNonce, error) {
	commitmentBytes := make([][]byte, len(commitments))
	for i, commitment := range commitments {
		commitmentBytes[i] = commitment.MarshalBinary()
	}
	noncesResult, err := common.GetDbFromContext(ctx).SigningNonce.Query().Where(signingnonce.NonceCommitmentIn(commitmentBytes...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[[66]byte]*objects.SigningNonce)
	for _, nonce := range noncesResult {
		signingNonce := objects.SigningNonce{}
		err = signingNonce.UnmarshalBinary(nonce.Nonce)
		if err != nil {
			return nil, err
		}
		result[[66]byte(nonce.NonceCommitment)] = &signingNonce
	}
	return result, nil
}
