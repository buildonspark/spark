package ent_utils

import (
	"context"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
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
