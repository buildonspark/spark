package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/tokentransactionreceipt"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/utils"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The TokenTransactionHandler is responsible for handling token transaction requests to spend and create leaves.
type TokenTransactionHandler struct {
	config authz.Config
}

// NewTokenTransactionHandler creates a new TokenTransactionHandler.
func NewTokenTransactionHandler(config authz.Config) *TokenTransactionHandler {
	return &TokenTransactionHandler{
		config: config,
	}
}

func validateStartTokenTransactionOperatorResponses(response map[string]interface{}) (*pb.TokenTransaction, error) {
	var expectedHash []byte
	var finalTx *pb.TokenTransaction

	for operatorID, resp := range response {
		txResponse, ok := resp.(*pbinternal.StartTokenTransactionInternalResponse)
		if !ok {
			return nil, fmt.Errorf("unexpected transaction from operator %s", operatorID)
		}
		currentHash, err := utils.HashTokenTransaction(txResponse.FinalTokenTransaction, false)
		if err != nil {
			return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
		}
		if expectedHash == nil {
			finalTx = txResponse.FinalTokenTransaction
			expectedHash = currentHash
			continue
		}
		if !bytes.Equal(expectedHash, currentHash) {
			return nil, fmt.Errorf(
				"inconsistent token transaction hash from operators - expected %x but got %x from operator %s",
				expectedHash,
				currentHash,
				operatorID,
			)
		}
	}
	return finalTx, nil
}

// StartTokenTransaction verifies the token leaves, generates the keyshares for the token transaction, and returns metadata about the operators that possess the keyshares.
func (o TokenTransactionHandler) StartTokenTransaction(ctx context.Context, config *so.Config, req *pb.StartTokenTransactionRequest) (*pb.StartTokenTransactionResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	if err := utils.ValidatePartialTokenTransaction(req.PartialTokenTransaction, req.TokenTransactionSignatures, config.GetSigningOperatorList()); err != nil {
		return nil, err
	}

	// TODO: Add a call to the LRC20 node to verify the validity of the transaction payload.

	// Each created leaf requires a keyshare for revocation key generation.
	numRevocationKeysharesNeeded := len(req.PartialTokenTransaction.OutputLeaves)
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, config, numRevocationKeysharesNeeded)
	if err != nil {
		return nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}

	// Save the token transaction object to lock in the revocation public keys for each created leaf within this transaction.
	// Note that atomicity here is very important to ensure that the unused keyshares queried above are not used by another operation.
	// This property should be help because the coordinator blocks on the other SO responses.
	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	response, err := helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnectionWithCert(operator.Address, operator.CertPath)
		if err != nil {
			log.Printf("Failed to connect to operator for marking token transaction keyshare: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.StartTokenTransactionInternal(ctx, &pbinternal.StartTokenTransactionInternalRequest{
			KeyshareIds:                keyshareIDStrings,
			PartialTokenTransaction:    req.PartialTokenTransaction,
			TokenTransactionSignatures: req.TokenTransactionSignatures,
		})
		if err != nil {
			log.Printf("Failed to execute start token transaction task with operator %s: %v", operator.Identifier, err)
			return nil, err
		}
		return internalResp, err
	})
	if err != nil {
		log.Printf("Failed to execute start token transaction task with all operators: %v", err)
		return nil, err
	}

	finalTokenTransaction, err := validateStartTokenTransactionOperatorResponses(response)
	if err != nil {
		return nil, err
	}

	operatorList, err := allSelection.OperatorList(config)
	if err != nil {
		log.Printf("Failed to get selection operator list: %v", err)
		return nil, err
	}
	operatorIdentifiers := make([]string, len(operatorList))
	for i, operator := range operatorList {
		operatorIdentifiers[i] = operator.Identifier
	}
	signingKeyshareInfo := &pb.SigningKeyshare{
		OwnerIdentifiers: operatorIdentifiers,
		// TODO: Unify threshold type (uint32 vs uint64) at all callsites between protos and config.
		Threshold: uint32(config.Threshold),
	}

	return &pb.StartTokenTransactionResponse{
		FinalTokenTransaction: finalTokenTransaction,
		KeyshareInfo:          signingKeyshareInfo,
	}, nil
}

// SignTokenTransaction signs the token transaction with the operators private key.
// If it is a transfer it also fetches this operators keyshare for each spent leaf and
// returns it to the wallet so it can finalize the transaction.
func (o TokenTransactionHandler) SignTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.SignTokenTransactionRequest,
) (*pb.SignTokenTransactionResponse, error) {
	// TODO: Add authz

	// Validate each leaf signature in the request. Each signed payload consists of
	//   payload.final_token_transaction_hash
	//   payload.operator_identity_public_key
	// To verify that this request for this transaction came from the leaf owner
	// (who is about to transfer the leaf once receiving all shares).
	for _, leafSig := range req.OperatorSpecificSignatures {
		payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(leafSig.Payload)
		if err != nil {
			return nil, fmt.Errorf("failed to hash revocation keyshares payload: %w", err)
		}

		if err := utils.ValidateOwnershipSignature(
			leafSig.OwnerSignature,
			payloadHash,
			leafSig.OwnerPublicKey,
		); err != nil {
			return nil, fmt.Errorf("invalid owner signature for leaf: %w", err)
		}
	}
	tokenTransactionReceipt, err := ent.GetDbFromContext(ctx).TokenTransactionReceipt.Query().
		Where(tokentransactionreceipt.FinalizedTokenTransactionHash(req.FinalTokenTransactionHash)).
		WithCreatedLeaf().
		WithSpentLeaf().
		Only(ctx)
	if err != nil {
		log.Printf("Sign request for token transaction did not map to a previously started transaction: %v", err)
		return nil, err
	}

	// Sign the token transaction hash with the operator identity private key.
	identityPrivateKey := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey)
	operatorSignature := ecdsa.Sign(identityPrivateKey, req.FinalTokenTransactionHash)
	if err != nil {
		log.Printf("Failed to sign token transaction with operator key: %v", err)
		return nil, err
	}

	newCreatedLeafStatus := schema.TokenLeafStatusCreatedSigned
	// If there are no spent leaves, it means the transaction is an issuance. These transctions don't require
	// a finalize step so we can mark it as immediately final.
	if len(tokenTransactionReceipt.Edges.SpentLeaf) == 0 {
		newCreatedLeafStatus = schema.TokenLeafStatusCreatedFinalized
	}
	err = ent.UpdateLeafStatuses(ctx, tokenTransactionReceipt.Edges.CreatedLeaf, newCreatedLeafStatus)
	if err != nil {
		log.Printf("Failed to update created leaf statuses to CreatedSigned: %v", err)
		return nil, err
	}
	err = ent.UpdateLeafStatuses(ctx, tokenTransactionReceipt.Edges.SpentLeaf, schema.TokenLeafStatusSpentSigned)
	if err != nil {
		log.Printf("Failed to update spent leaf statuses to SpentSigned: %v", err)
	}

	var keyshares []*ent.SigningKeyshare
	for _, leaf := range tokenTransactionReceipt.Edges.SpentLeaf {
		keyshare, err := leaf.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			log.Printf("Failed to get keyshare for leaf: %v", err)
			return nil, err
		}
		keyshares = append(keyshares, keyshare)

		// Validate that the keyshare's public key matches the leaf's revocation public key.
		if !bytes.Equal(keyshare.PublicKey, leaf.WithdrawalRevocationPublicKey) {
			return nil, fmt.Errorf(
				"keyshare public key %x does not match leaf revocation public key %x",
				keyshare.PublicKey,
				leaf.WithdrawalRevocationPublicKey,
			)
		}
	}

	revocationKeyshares := make([][]byte, len(keyshares))
	for i, keyshare := range keyshares {
		revocationKeyshares[i] = keyshare.SecretShare
	}

	return &pb.SignTokenTransactionResponse{
		SparkOperatorSignature:              operatorSignature.Serialize(),
		TokenTransactionRevocationKeyshares: revocationKeyshares,
	}, nil
}

// FinalizeTokenTransaction takes the revocation private keys for spent leaves and updates their status to finalized.
func (o TokenTransactionHandler) FinalizeTokenTransaction(
	ctx context.Context,
	req *pb.FinalizeTokenTransactionRequest,
) (*emptypb.Empty, error) {
	db := ent.GetDbFromContext(ctx)
	finalTokenTransactionHash := req.FinalTokenTransactionHash

	// Query the token_transaction_receipt by the final transaction hash.
	receipt, err := db.TokenTransactionReceipt.Query().
		Where(tokentransactionreceipt.FinalizedTokenTransactionHash(finalTokenTransactionHash)).
		WithSpentLeaf().
		Only(ctx)
	if err != nil {
		log.Printf("Failed to fetch matching transaction receipt: %v", err)
		return nil, fmt.Errorf("failed to fetch transaction receipt: %w", err)
	}

	spentLeaves := receipt.Edges.SpentLeaf
	if len(spentLeaves) == 0 {
		return nil, fmt.Errorf("no spent leaves found for transaction hash %x", finalTokenTransactionHash)
	}

	// Validate that we have the right number of revocation keys
	if len(req.LeafToSpendRevocationKeys) != len(spentLeaves) {
		return nil, fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent leaves (%d)",
			len(req.LeafToSpendRevocationKeys),
			len(spentLeaves),
		)
	}

	// TODO: Validate that the revocation private key is correct.

	// Update all spent leaves with their revocation private keys and set status to finalized
	leafIDs := make([]uuid.UUID, len(spentLeaves))
	for i, leaf := range spentLeaves {
		leafIDs[i] = leaf.ID
		// Update each leaf individually to set the revocation private key
		if _, err := db.TokenLeaf.UpdateOne(leaf).
			SetLeafSpentRevocationPrivateKey(req.LeafToSpendRevocationKeys[i]).
			SetStatus(schema.TokenLeafStatusSpentFinalized).
			Save(ctx); err != nil {
			log.Printf("Failed to update leaf %s with revocation private key: %v", leaf.ID, err)
			return nil, fmt.Errorf("failed to update leaf with revocation key: %w", err)
		}
	}

	return &emptypb.Empty{}, nil
}
