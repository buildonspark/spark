package ent

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenfreeze"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

func GetActiveFreezes(ctx context.Context, ownerPublicKeys []keys.Public, tokenCreateId uuid.UUID) ([]*TokenFreeze, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	activeFreezes, err := db.TokenFreeze.Query().Where(
		tokenfreeze.OwnerPublicKeyIn(ownerPublicKeys...),
		tokenfreeze.StatusEQ(st.TokenFreezeStatusFrozen),
		tokenfreeze.TokenCreateID(tokenCreateId),
	).All(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch active freezes for token_create_id %s and owner_public_keys %+q: %w", tokenCreateId, ownerPublicKeys, err))
	}
	return activeFreezes, nil
}

func ThawActiveFreeze(ctx context.Context, activeFreezeID uuid.UUID, timestamp uint64) error {
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
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to thaw active freeze %s at timestamp %d: %w", activeFreezeID, timestamp, err))
	}
	return nil
}

func ActivateFreeze(ctx context.Context, ownerPublicKey keys.Public, tokenCreateID uuid.UUID, issuerSignature []byte, timestamp uint64) error {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	_, err = db.TokenFreeze.Create().
		SetStatus(st.TokenFreezeStatusFrozen).
		SetOwnerPublicKey(ownerPublicKey).
		SetTokenCreateID(tokenCreateID).
		SetWalletProvidedFreezeTimestamp(timestamp).
		SetIssuerSignature(issuerSignature).
		Save(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to activate freeze (owner_public_key %s, token_create_id %s, timestamp %d, signature %s): %w", ownerPublicKey, tokenCreateID, timestamp, issuerSignature, err))
	}
	return nil
}

// GetMostRecentThawTimestamp returns the most recent thaw timestamp for a given owner and token.
// Returns 0 if no thaw has ever occurred.
func GetMostRecentThawTimestamp(ctx context.Context, ownerPublicKey keys.Public, tokenCreateID uuid.UUID) (uint64, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return 0, err
	}

	// Query for the most recent thawed freeze record
	freeze, err := db.TokenFreeze.Query().
		Where(
			tokenfreeze.OwnerPublicKey(ownerPublicKey),
			tokenfreeze.TokenCreateID(tokenCreateID),
			tokenfreeze.StatusEQ(st.TokenFreezeStatusThawed),
			tokenfreeze.WalletProvidedThawTimestampNotNil(),
		).
		Order(Desc(tokenfreeze.FieldWalletProvidedThawTimestamp)).
		First(ctx)
	if err != nil {
		if IsNotFound(err) {
			return 0, nil
		}
		return 0, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get most recent thaw timestamp: %w", err))
	}

	return freeze.WalletProvidedThawTimestamp, nil
}
