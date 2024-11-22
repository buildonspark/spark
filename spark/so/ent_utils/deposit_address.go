package ent_utils

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
)

func LinkKeyshareToDepositAddress(ctx context.Context, config *so.Config, keyshareID uuid.UUID, address string) (*ent.DepositAddress, error) {
	db, err := ent.Open(config.DatabaseDriver(), config.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	depositAddress, err := db.DepositAddress.Create().SetSigningKeyshareID(keyshareID).SetAddress(address).Save(ctx)
	if err != nil {
		return nil, err
	}

	return depositAddress, nil
}
