package ent_utils

import (
	"context"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/signingnonce"
	"github.com/lightsparkdev/spark-go/so/objects"
)

func StoreSigningNonce(ctx context.Context, config *so.Config, nonce objects.SigningNonce, commitment objects.SigningCommitment) error {
	db, err := ent.Open(config.DatabaseDriver(), config.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	nonceBytes, err := nonce.MarshalBinary()
	if err != nil {
		return err
	}
	commitmentBytes, err := commitment.MarshalBinary()
	if err != nil {
		return err
	}

	_, err = db.SigningNonce.Create().
		SetNonce(nonceBytes).
		SetNonceCommitment(commitmentBytes).
		Save(ctx)
	return err
}

func GetSigningNonceFromCommitment(ctx context.Context, config *so.Config, commitment objects.SigningCommitment) (*objects.SigningNonce, error) {
	db, err := ent.Open(config.DatabaseDriver(), config.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	commitmentBytes, err := commitment.MarshalBinary()
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
