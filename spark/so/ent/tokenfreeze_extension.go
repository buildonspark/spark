package ent

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/tokenfreeze"
)

func GetActiveFreezes(ctx context.Context, ownerPublicKeys [][]byte, tokenPublicKey []byte) ([]*TokenFreeze, error) {
	activeFreezes, err := GetDbFromContext(ctx).TokenFreeze.Query().
		Where(
			// Order matters here to leverage the index.
			tokenfreeze.OwnerPublicKeyIn(ownerPublicKeys...),
			tokenfreeze.TokenPublicKeyEQ(tokenPublicKey),
			tokenfreeze.StatusEQ(schema.TokenFreezeStatusFrozen),
		).All(ctx)
	if err != nil {
		log.Printf("Failed to fetch active freezes: %v", err)
		return nil, err
	}
	return activeFreezes, nil
}

func ThawActiveFreeze(ctx context.Context, activeFreezeID uuid.UUID, timestamp uint64) error {
	_, err := GetDbFromContext(ctx).TokenFreeze.Update().
		Where(
			tokenfreeze.IDEQ(activeFreezeID),
		).
		SetStatus(schema.TokenFreezeStatusThawed).
		SetWalletProvidedThawTimestamp(timestamp).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to thaw active freeze: %v", err)
		return err
	}
	return nil
}

func ActivateFreeze(ctx context.Context, ownerPublicKey []byte, tokenPublicKey []byte, issuerSignature []byte, timestamp uint64) error {
	_, err := GetDbFromContext(ctx).TokenFreeze.Create().
		SetStatus(schema.TokenFreezeStatusFrozen).
		SetOwnerPublicKey(ownerPublicKey).
		SetTokenPublicKey(tokenPublicKey).
		SetWalletProvidedFreezeTimestamp(timestamp).
		SetIssuerSignature(issuerSignature).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to activate freeze: %v", err)
		return err
	}
	return nil
}
