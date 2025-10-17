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

func getTokenIdentifierFromTransaction(tokenTransaction *tokenpb.TokenTransaction) common.TokenIdentifier {
	// For transactions with token identifier set in outputs
	if len(tokenTransaction.TokenOutputs) > 0 && tokenTransaction.TokenOutputs[0].GetTokenIdentifier() != nil {
		return tokenTransaction.TokenOutputs[0].GetTokenIdentifier()
	}
	return nil
}

func getIssuerPublicKeyFromTransaction(tokenTransaction *tokenpb.TokenTransaction) (keys.Public, error) {
	// For transactions with token public key set in outputs
	if len(tokenTransaction.TokenOutputs) > 0 && tokenTransaction.TokenOutputs[0].GetTokenPublicKey() != nil {
		return keys.ParsePublicKey(tokenTransaction.TokenOutputs[0].GetTokenPublicKey())
	}
	if tokenTransaction.GetCreateInput().GetIssuerPublicKey() != nil {
		return keys.ParsePublicKey(tokenTransaction.GetCreateInput().GetIssuerPublicKey())
	}
	return keys.Public{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no token identifier or issuer public key found for token transaction: %v", tokenTransaction))
}

// GetTokenMetadataForTokenTransaction returns the token metadata for the given token transaction.
// It searches for the token metadata in the TokenCreate table.
func GetTokenMetadataForTokenTransaction(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) (*common.TokenMetadata, error) {
	tx, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	tokenIdentifier := getTokenIdentifierFromTransaction(tokenTransaction)
	if tokenIdentifier != nil {
		tokenCreate, err := tx.TokenCreate.Query().Where(tokencreate.TokenIdentifierEQ(tokenIdentifier)).First(ctx)
		if err == nil {
			return tokenCreate.ToTokenMetadata()
		}
		if !IsNotFound(err) {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("error querying TokenCreate table: %w", err))
		}
		return nil, nil
	}

	issuerPublicKey, err := getIssuerPublicKeyFromTransaction(tokenTransaction)
	if err != nil {
		return nil, err
	}

	network, err := common.NetworkFromProtoNetwork(tokenTransaction.Network)
	if err != nil {
		return nil, err
	}
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
	if err != nil {
		return nil, err
	}
	tokenCreate, err := tx.TokenCreate.Query().Where(tokencreate.IssuerPublicKeyEQ(issuerPublicKey), tokencreate.NetworkEQ(schemaNetwork)).First(ctx)
	if err == nil {
		return tokenCreate.ToTokenMetadata()
	}
	if !IsNotFound(err) {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("error querying TokenCreate table: %w", err))
	}
	return nil, nil
}
