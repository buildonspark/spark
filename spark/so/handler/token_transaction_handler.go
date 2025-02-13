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
	pblrc20 "github.com/lightsparkdev/spark-go/proto/lrc20"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
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
	finalTokenTransactionHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}
	tokenTransactionReceipt, err := ent.FetchTokenTransactionData(ctx, finalTokenTransactionHash)
	if err != nil {
		log.Printf("Sign request for token transaction did not map to a previously started transaction: %v", err)
		return nil, err
	}

	// Sign the token transaction hash with the operator identity private key.
	identityPrivateKey := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey)
	operatorSignature := ecdsa.Sign(identityPrivateKey, finalTokenTransactionHash)

	operatorSpecificSignature := make([][]byte, len(req.OperatorSpecificSignatures))
	for i, sig := range req.OperatorSpecificSignatures {
		operatorSpecificSignature[i] = sig.OwnerSignature
	}
	err = ent.UpdateSignedTransactionLeaves(ctx, tokenTransactionReceipt, operatorSpecificSignature, operatorSignature.Serialize())
	if err != nil {
		log.Printf("Failed to update leaves after signing: %v", err)
		return nil, err
	}

	err = o.SendTransactionToLRC20Node(
		ctx,
		config,
		req.FinalTokenTransaction,
		operatorSignature.Serialize(),
		// Revocation keys not available until finalize step.
		[][]byte{})
	if err != nil {
		log.Printf("Failed to send transaction to LRC20 node: %v", err)
		return nil, err
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
	config *so.Config,
	req *pb.FinalizeTokenTransactionRequest,
) (*emptypb.Empty, error) {
	finalTokenTransactionHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}

	tokenTransactionReceipt, err := ent.FetchTokenTransactionData(ctx, finalTokenTransactionHash)
	if err != nil {
		log.Printf("Failed to fetch matching transaction receipt: %v", err)
		return nil, fmt.Errorf("failed to fetch transaction receipt: %w", err)
	}

	// Extract revocation public keys from spent leaves
	revocationPublicKeys := make([][]byte, len(tokenTransactionReceipt.Edges.SpentLeaf))
	if (len(tokenTransactionReceipt.Edges.SpentLeaf)) != len(req.LeafToSpendRevocationKeys) {
		return nil, fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent leaves (%d)",
			len(req.LeafToSpendRevocationKeys),
			len(tokenTransactionReceipt.Edges.SpentLeaf),
		)
	}
	for _, leaf := range tokenTransactionReceipt.Edges.SpentLeaf {
		revocationPublicKeys[leaf.LeafSpentTransactionInputVout] = leaf.WithdrawalRevocationPublicKey
	}
	err = utils.ValidateRevocationKeys(req.LeafToSpendRevocationKeys, revocationPublicKeys)
	if err != nil {
		return nil, err
	}

	err = o.SendTransactionToLRC20Node(
		ctx,
		config,
		req.FinalTokenTransaction,
		// TODO: Consider removing this because it was already provided in the Sign() step.
		tokenTransactionReceipt.OperatorSignature,
		req.LeafToSpendRevocationKeys)
	if err != nil {
		log.Printf("Failed to send transaction to LRC20 node: %v", err)
		return nil, err
	}

	err = ent.UpdateFinalizedTransactionLeaves(ctx, tokenTransactionReceipt, req.LeafToSpendRevocationKeys)
	if err != nil {
		log.Printf("Failed to update leaves after finalizing: %v", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (o TokenTransactionHandler) SendTransactionToLRC20Node(
	ctx context.Context,
	config *so.Config,
	finalTokenTransaction *pb.TokenTransaction,
	operatorSignature []byte,
	revocationKeys [][]byte,
) error {
	leavesToSpendData := make([]*pblrc20.SparkSignatureLeafData, len(revocationKeys))
	for i, revocationKey := range revocationKeys {
		leavesToSpendData[i] = &pblrc20.SparkSignatureLeafData{
			SpentLeafIndex: uint32(i),
			// Revocation will be nil if we are sending the transaction at the Sign() step.
			// It will be filled when sending a transaction in the Finalize() step.
			RevocationPrivateKey: revocationKey,
		}
	}

	signatureData := &pblrc20.SparkSignatureData{
		SparkOperatorSignature:         operatorSignature,
		SparkOperatorIdentityPublicKey: config.IdentityPublicKey(),
		FinalTokenTransaction:          finalTokenTransaction,
		LeavesToSpendData:              leavesToSpendData,
	}

	conn, err := helper.ConnectToLrc20Node(config)
	if err != nil {
		return fmt.Errorf("failed to connect to LRC20 node: %w", err)
	}
	defer conn.Close()
	lrc20Client := pblrc20.NewSparkServiceClient(conn)

	_, err = lrc20Client.SendSparkSignature(ctx, &pblrc20.SendSparkSignatureRequest{
		SignatureData: signatureData,
	})
	if err != nil {
		return err
	}

	return nil
}
