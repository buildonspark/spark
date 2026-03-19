package ent

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// GetTokenCreateByIdentifier returns the TokenCreate entity for the given token identifier.
func GetTokenCreateByIdentifier(ctx context.Context, tokenIdentifier []byte) (*TokenCreate, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return db.TokenCreate.Query().Where(tokencreate.TokenIdentifier(tokenIdentifier)).Only(ctx)
}

// GetIssuerPublicKeyByTokenIdentifier looks up the issuer public key for a token by its identifier.
func GetIssuerPublicKeyByTokenIdentifier(ctx context.Context, tokenIdentifier []byte) (keys.Public, error) {
	tokenCreate, err := GetTokenCreateByIdentifier(ctx, tokenIdentifier)
	if err != nil {
		return keys.Public{}, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to look up token create by identifier: %w", err))
	}
	return tokenCreate.IssuerPublicKey, nil
}

// GetTokenCreateByIdentifierForUpdate returns the TokenCreate entity with a FOR UPDATE lock.
// Use this when you need to prevent concurrent modifications to freeze state for a token.
func GetTokenCreateByIdentifierForUpdate(ctx context.Context, tokenIdentifier []byte) (*TokenCreate, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return db.TokenCreate.Query().Where(tokencreate.TokenIdentifier(tokenIdentifier)).ForUpdate().Only(ctx)
}

// GetTokenMetadataForTokenTransaction extracts token identifiers from a token transaction
// and returns the corresponding TokenMetadata slice. Returns (nil, nil) if no token identifiers
// are found or the tokens do not exist.
func GetTokenMetadataForTokenTransaction(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) ([]*common.TokenMetadata, error) {
	var tokenIdentifier []byte
	if len(tokenTransaction.GetTokenOutputs()) > 0 {
		tokenIdentifier = tokenTransaction.GetTokenOutputs()[0].GetTokenIdentifier()
	}
	if len(tokenIdentifier) == 0 {
		return nil, nil
	}

	tokenCreate, err := GetTokenCreateByIdentifier(ctx, tokenIdentifier)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to look up token by identifier: %w", err))
	}

	metadata, err := tokenCreate.ToTokenMetadata()
	if err != nil {
		return nil, err
	}
	return []*common.TokenMetadata{metadata}, nil
}
