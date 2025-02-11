package wallet

import (
	"context"
	"fmt"
	"log"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/utils"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// KeyshareWithOperatorIndex holds a keyshare and its corresponding operator index
type KeyshareWithOperatorIndex struct {
	Keyshare []byte
	Index    uint64
}

// BrodcastTokenTransaction starts and finalizes a token transaction.
func BroadcastTokenTransaction(
	ctx context.Context,
	config *Config,
	tokenTransaction *pb.TokenTransaction,
) (*pb.TokenTransaction, error) {
	sparkConn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoodinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	signingOperatorResponse, err := sparkClient.GetSigningOperatorList(tmpCtx, &emptypb.Empty{})
	if err != nil {
		log.Printf("Error while calling GetSigningOperatorList: %v", err)
		return nil, err
	}
	var operatorKeys [][]byte
	for _, operator := range signingOperatorResponse.SigningOperators {
		operatorKeys = append(operatorKeys, operator.PublicKey)
	}

	tokenTransaction.SparkOperatorIdentityPublicKeys = operatorKeys

	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		log.Printf("Error while hashing partial token transaction: %v", err)
		return nil, err
	}

	var ownerSignatures [][]byte
	if tokenTransaction.GetIssueInput() != nil {
		signingPrivKeySecp := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize())
		ownerSignatures = append(ownerSignatures,
			ecdsa.Sign(signingPrivKeySecp, partialTokenTransactionHash).Serialize())
	}
	startResponse, err := sparkClient.StartTokenTransaction(tmpCtx, &pb.StartTokenTransactionRequest{
		IdentityPublicKey:       config.IdentityPublicKey(),
		PartialTokenTransaction: tokenTransaction,
		TokenTransactionSignatures: &pb.TokenTransactionSignatures{
			OwnerSignatures: ownerSignatures,
		},
	})
	if err != nil {
		log.Printf("Error while calling StartTokenTransaction: %v", err)
		return nil, err
	}
	// Validate that the keyshare config returned by the coordinator SO matches the full signing operator list.
	// TODO: When we support threshold signing allow the keyshare identifiers to be a subset of the signing operators.
	if len(startResponse.KeyshareInfo.OwnerIdentifiers) != len(signingOperatorResponse.SigningOperators) {
		return nil, fmt.Errorf("keyshare operator count (%d) does not match signing operator count (%d)",
			len(startResponse.KeyshareInfo.OwnerIdentifiers), len(signingOperatorResponse.SigningOperators))
	}
	for _, operator := range startResponse.KeyshareInfo.OwnerIdentifiers {
		if _, exists := signingOperatorResponse.SigningOperators[operator]; !exists {
			return nil, fmt.Errorf("keyshare operator %s not found in signing operator list", operator)
		}
	}

	// Validate that the operator signatures match the provided operator keys
	finalTokenTransactionHash, err := utils.HashTokenTransaction(startResponse.FinalTokenTransaction, false)
	if err != nil {
		log.Printf("Error while hashing final token transaction: %v", err)
		return nil, err
	}

	var operatorSpecificSignatures []*pb.OperatorSpecificTokenTransactionSignature
	// Create signable payload
	payload := &pb.OperatorSpecificTokenTransactionSignablePayload{
		FinalTokenTransactionHash: finalTokenTransactionHash,
		OperatorIdentityPublicKey: config.IdentityPublicKey(),
	}

	payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(payload)
	if err != nil {
		log.Printf("Error while hashing revocation keyshares payload: %v", err)
		return nil, err
	}
	// For issue transactions, create a single operator-specific signature using the issuer private key
	if tokenTransaction.GetIssueInput() != nil {
		// Sign with the issuer's private key
		sig := ecdsa.Sign(secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize()), payloadHash)
		operatorSpecificSignatures = append(operatorSpecificSignatures, &pb.OperatorSpecificTokenTransactionSignature{
			OwnerPublicKey: config.IdentityPublicKey(),
			OwnerSignature: sig.Serialize(),
			Payload:        payload,
		})
	}

	// Collect keyshares from each operator
	for _, operator := range signingOperatorResponse.SigningOperators {
		operatorConn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to operator at %s: %v", operator.Address, err)
			return nil, err
		}
		defer operatorConn.Close()

		operatorClient := pb.NewSparkServiceClient(operatorConn)
		signTokenTransactionResponse, err := operatorClient.SignTokenTransaction(ctx,
			&pb.SignTokenTransactionRequest{
				FinalTokenTransactionHash:  finalTokenTransactionHash,
				OperatorSpecificSignatures: operatorSpecificSignatures,
			})
		if err != nil {
			log.Printf("Error while calling SignTokenTransaction with operator %s: %v", operator.Identifier, err)
			return nil, err
		}

		operatorSig := signTokenTransactionResponse.SparkOperatorSignature
		if err := utils.ValidateOwnershipSignature(operatorSig, finalTokenTransactionHash, operator.PublicKey); err != nil {
			return nil, fmt.Errorf("invalid signature from operator with public key %x: %v", operator.PublicKey, err)
		}
	}

	return startResponse.FinalTokenTransaction, nil
}
