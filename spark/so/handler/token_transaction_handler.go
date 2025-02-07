package handler

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/utils"
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

// generateTokenTransactionKeyshare reserves new keyshares for revocation keys on created leaves and
// recovers the revocation keys from the distributed keyshares for spent leaves.
func (o *TokenTransactionHandler) generateAndResolveRevocationKeys(
	ctx context.Context,
	config *so.Config,
	tokenTransaction *pb.TokenTransaction,
) (*pb.TokenTransaction, []string, *pb.SigningKeyshare, error) {
	// Each created leaf requires a keyshare for revocation key generation.
	numRevocationKeysharesNeeded := len(tokenTransaction.GetOutputLeaves())

	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, config, numRevocationKeysharesNeeded)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(keyshares) < numRevocationKeysharesNeeded {
		return nil, nil, nil, fmt.Errorf("Not enough keyshares available for token transaction")
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}
	err = ent.MarkSigningKeysharesAsUsed(ctx, config, keyshareIDs)

	finalTokenTransaction := tokenTransaction
	if err != nil {
		log.Printf("Failed to mark keyshare as used: %v", err)
		return nil, nil, nil, err
	}

	// Mark keyshares as used in the non-coordinator SO's.
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Failed to connect to operator for marking token transaction keyshare: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeysharesAsUsed(ctx, &pbinternal.MarkKeysharesAsUsedRequest{KeyshareId: keyshareIDStrings})
		return nil, err
	})
	if err != nil {
		log.Printf("Failed to execute mark token transaction keyshare task with all operators: %v", err)
		return nil, nil, nil, err
	}

	// Fill the used keyshare public keys as revocation public keys in the token transaction.
	for i := 0; i < numRevocationKeysharesNeeded; i++ {
		finalTokenTransaction.OutputLeaves[i].RevocationPublicKey = keyshares[i].PublicKey
	}

	operatorList, err := selection.OperatorList(config)
	if err != nil {
		log.Printf("Failed to get operator list: %v", err)
		return nil, nil, nil, err
	}
	operatorIdentifiers := make([]string, len(config.GetSigningOperatorList()))
	for i, operator := range operatorList {
		operatorIdentifiers[i] = operator.Identifier
	}

	signingKeyshare := &pb.SigningKeyshare{
		OwnerIdentifiers: operatorIdentifiers,
		// TODO: Unify threshold type (uint32 vs uint64) at all callsites between protos and config.
		Threshold: uint32(config.Threshold),
	}

	// Return final token transaction.
	return finalTokenTransaction, keyshareIDStrings, signingKeyshare, nil
}

// StartTokenTransaction verifies the token leaves, generates the keyshares for the token transaction, and returns the signature shares for the token transaction payload.
func (o TokenTransactionHandler) StartTokenTransaction(ctx context.Context, config *so.Config, req *pb.StartTokenTransactionRequest) (*pb.StartTokenTransactionResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	if err := utils.ValidatePartialTokenTransaction(req.PartialTokenTransaction, req.TokenTransactionSignatures, config.GetSigningOperatorList()); err != nil {
		return nil, err
	}

	// TODO: Add a call to the LRC20 node to verify the validity of the transaction payload.

	finalTokenTransaction, keyshareIDStrings, keyshareInfo, err := o.generateAndResolveRevocationKeys(ctx, config, req.GetPartialTokenTransaction())
	if err != nil {
		return nil, err
	}

	// Ask all the coordinators to sign the token transaction
	signingOperatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	signatureResponses, err := helper.ExecuteTaskWithAllOperators(ctx, config, &signingOperatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Failed to connect to operator for marking token transaction keyshare: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		response, err := client.SignTokenTransaction(ctx, &pbinternal.SignTokenTransactionRequest{
			TokenTransaction:                finalTokenTransaction,
			TokenTransactionSignatures:      req.TokenTransactionSignatures,
			OutputLeafRevocationKeyshareIds: keyshareIDStrings})
		return response, err
	})
	if err != nil {
		log.Printf("Failed to execute mark token transaction keyshare task with all operators: %v", err)
		return nil, err
	}

	// Convert the signing operator list to a map of identifier to identity public key
	sparkOperatorSignatures := make(map[string][]byte)
	for identifier, signatureResponse := range signatureResponses {
		sparkOperatorSignatures[identifier] = signatureResponse.(*pbinternal.SignTokenTransactionResponse).OperatorSignature
	}

	// TODO: Add a call to the LRC20 node to finalize the transaction payload.

	return &pb.StartTokenTransactionResponse{
		FinalTokenTransaction:   finalTokenTransaction,
		SparkOperatorSignatures: sparkOperatorSignatures,
		KeyshareInfo:            keyshareInfo,
	}, nil
}
