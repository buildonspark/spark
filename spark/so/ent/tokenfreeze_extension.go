package ent

import (
	"context"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenfreeze"
)

func GetActiveFreezes(ctx context.Context, ownerPublicKeys []keys.Public, tokenCreateId uuid.UUID) ([]*TokenFreeze, error) {
	logger := logging.GetLoggerFromContext(ctx)

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	ownerPubKeyBytes := make([][]byte, len(ownerPublicKeys))
	for i, ownerPublicKey := range ownerPublicKeys {
		ownerPubKeyBytes[i] = ownerPublicKey.Serialize()
	}
	conditions := []predicate.TokenFreeze{
		tokenfreeze.OwnerPublicKeyIn(ownerPubKeyBytes...),
		tokenfreeze.StatusEQ(st.TokenFreezeStatusFrozen),
		tokenfreeze.TokenCreateID(tokenCreateId),
	}

	activeFreezes, err := db.TokenFreeze.Query().Where(conditions...).All(ctx)
	if err != nil {
		logger.Error("Failed to fetch active freezes", "error", err, "ownerPublicKeys", ownerPublicKeys, "tokenCreateId", tokenCreateId)
		return nil, err
	}
	return activeFreezes, nil
}

func ThawActiveFreeze(ctx context.Context, activeFreezeID uuid.UUID, timestamp uint64) error {
	logger := logging.GetLoggerFromContext(ctx)

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	_, err = db.TokenFreeze.Update().
		Where(tokenfreeze.IDEQ(activeFreezeID)).
		SetStatus(st.TokenFreezeStatusThawed).
		SetWalletProvidedThawTimestamp(timestamp).
		Save(ctx)
	if err != nil {
		logger.Error("Failed to thaw active freeze", "error", err, "activeFreezeID", activeFreezeID, "timestamp", timestamp)
		return err
	}
	return nil
}

func ActivateFreeze(ctx context.Context, ownerPublicKey keys.Public, tokenCreateID uuid.UUID, issuerSignature []byte, timestamp uint64) error {
	logger := logging.GetLoggerFromContext(ctx)

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	_, err = db.TokenFreeze.Create().
		SetStatus(st.TokenFreezeStatusFrozen).
		SetOwnerPublicKey(ownerPublicKey.Serialize()).
		SetTokenCreateID(tokenCreateID).
		SetWalletProvidedFreezeTimestamp(timestamp).
		SetIssuerSignature(issuerSignature).
		Save(ctx)
	if err != nil {
		logger.Error("Failed to activate freeze", "error", err, "ownerPublicKey", ownerPublicKey, "tokenCreateID", tokenCreateID, "timestamp", timestamp, "issuerSignature", issuerSignature)
		return err
	}
	return nil
}
