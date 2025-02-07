package handler

import (
	"context"
	"log"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark-go/so/utils"
)

// InternalTokenTransactionHandler is the deposit handler for so internal
type InternalTokenTransactionHandler struct {
	config *so.Config
}

// NewInternalTokenTransactionHandler creates a new InternalTokenTransactionHandler.
func NewInternalTokenTransactionHandler(config *so.Config) *InternalTokenTransactionHandler {
	return &InternalTokenTransactionHandler{config: config}
}

//  1. Validates that the wallet requesting the token transaction has either a) ownership of the token for issuance or
//     b) ownership of the leaves to spend for transfers.
//  2. Associates keyshares across SO's for a particular signing job (which could authorize issuance or spending of a leaf).
func (h *InternalTokenTransactionHandler) SignTokenTransaction(ctx context.Context, req *pbinternal.SignTokenTransactionRequest) (*pbinternal.SignTokenTransactionResponse, error) {
	db := ent.GetDbFromContext(ctx)
	// Query revocation public keys for the provided keyshare IDs to validate the filled values.
	expectedRevocationPublicKeys := make([][]byte, len(req.OutputLeafRevocationKeyshareIds))
	for i, keyshareIDStr := range req.OutputLeafRevocationKeyshareIds {
		keyshareID, err := uuid.Parse(keyshareIDStr)
		if err != nil {
			log.Printf("Failed to parse revocation keyshare ID: %v", err)
			return nil, err
		}
		keyshare, err := db.SigningKeyshare.Query().Where(signingkeyshare.ID(keyshareID)).Only(ctx)
		if err != nil {
			log.Printf("Failed to get revocation keyshare: %v", err)
			return nil, err
		}
		expectedRevocationPublicKeys[i] = keyshare.PublicKey
	}
	err := utils.ValidateFinalTokenTransaction(req.TokenTransaction, req.TokenTransactionSignatures, h.config.GetSigningOperatorList(), expectedRevocationPublicKeys)
	if err != nil {
		log.Printf("Failed to validate final token transaction: %v", err)
		return nil, err
	}

	finalTokenTransactionHash, err := utils.HashTokenTransaction(req.TokenTransaction)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}

	// Sign the token transaction hash with the operator identity private key.
	identityPrivateKey := secp256k1.PrivKeyFromBytes(h.config.IdentityPrivateKey)
	operatorSignature := ecdsa.Sign(identityPrivateKey, finalTokenTransactionHash)
	if err != nil {
		log.Printf("Failed to sign token transaction with operator key: %v", err)
		return nil, err
	}

	return &pbinternal.SignTokenTransactionResponse{
		OperatorSignature: operatorSignature.Serialize(),
	}, nil
}
