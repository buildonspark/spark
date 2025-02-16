package wallet

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/utils"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
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
	leafToSpendPrivateKeys []*secp256k1.PrivateKey,
	leafToSpendRevocationPublicKeys [][]byte,
) (*pb.TokenTransaction, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
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

	var operatorKeys [][]byte
	for _, operator := range config.SigningOperators {
		operatorKeys = append(operatorKeys, operator.IdentityPublicKey)
	}

	tokenTransaction.SparkOperatorIdentityPublicKeys = operatorKeys

	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		log.Printf("Error while hashing partial token transaction: %v", err)
		return nil, err
	}

	var ownerSignatures [][]byte
	if tokenTransaction.GetMintInput() != nil {
		signingPrivKeySecp := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize())
		ownerSignatures = append(ownerSignatures,
			ecdsa.Sign(signingPrivKeySecp, partialTokenTransactionHash).Serialize())
	} else if tokenTransaction.GetTransferInput() != nil {
		// For a transfer transaction, one signature for each leaf.
		for i := range leafToSpendPrivateKeys {
			ownerSignatures = append(ownerSignatures,
				ecdsa.Sign(leafToSpendPrivateKeys[i], partialTokenTransactionHash).Serialize())
		}
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
	if len(startResponse.KeyshareInfo.OwnerIdentifiers) != len(config.SigningOperators) {
		return nil, fmt.Errorf("keyshare operator count (%d) does not match signing operator count (%d)",
			len(startResponse.KeyshareInfo.OwnerIdentifiers), len(config.SigningOperators))
	}
	for _, operator := range startResponse.KeyshareInfo.OwnerIdentifiers {
		if _, exists := config.SigningOperators[operator]; !exists {
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
	if tokenTransaction.GetMintInput() != nil {
		// Sign with the issuer's private key
		sig := ecdsa.Sign(secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize()), payloadHash)
		operatorSpecificSignatures = append(operatorSpecificSignatures, &pb.OperatorSpecificTokenTransactionSignature{
			OwnerPublicKey: config.IdentityPublicKey(),
			OwnerSignature: sig.Serialize(),
			Payload:        payload,
		})
	}

	// For transfer transactions, create an operator-specific signature for each leaf.
	if tokenTransaction.GetTransferInput() != nil {
		// Create signatures for each leaf being spent
		for i := range tokenTransaction.GetTransferInput().GetLeavesToSpend() {
			// Sign with the leaf's private key
			sig := ecdsa.Sign(leafToSpendPrivateKeys[i], payloadHash)
			operatorSpecificSignatures = append(operatorSpecificSignatures, &pb.OperatorSpecificTokenTransactionSignature{
				OwnerPublicKey: leafToSpendPrivateKeys[i].PubKey().SerializeCompressed(),
				OwnerSignature: sig.Serialize(),
				Payload:        payload,
			})
		}
	}

	// Create a 2D slice to store keyshares and indices for each leaf from each operator.
	// This will be unfilled if its an issuance transaction.
	leafRevocationKeyshares := make([][]*KeyshareWithOperatorIndex, len(tokenTransaction.GetTransferInput().GetLeavesToSpend()))
	// Collect keyshares from each operator
	for _, operator := range config.SigningOperators {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to operator at %s: %v", operator.Address, err)
			return nil, err
		}
		defer operatorConn.Close()

		operatorClient := pb.NewSparkServiceClient(operatorConn)
		signTokenTransactionResponse, err := operatorClient.SignTokenTransaction(ctx,
			&pb.SignTokenTransactionRequest{
				FinalTokenTransaction:      startResponse.FinalTokenTransaction,
				OperatorSpecificSignatures: operatorSpecificSignatures,
			})
		if err != nil {
			log.Printf("Error while calling SignTokenTransaction with operator %s: %v", operator.Identifier, err)
			return nil, err
		}

		operatorSig := signTokenTransactionResponse.SparkOperatorSignature
		if err := utils.ValidateOwnershipSignature(operatorSig, finalTokenTransactionHash, operator.IdentityPublicKey); err != nil {
			return nil, fmt.Errorf("invalid signature from operator with public key %x: %v", operator.IdentityPublicKey, err)
		}
		// Store each leaf's keyshare and operator index
		for leafIndex, keyshare := range signTokenTransactionResponse.TokenTransactionRevocationKeyshares {
			leafRevocationKeyshares[leafIndex] = append(leafRevocationKeyshares[leafIndex], &KeyshareWithOperatorIndex{
				Keyshare: keyshare,
				Index:    parseHexIdentifierToUint64(operator.Identifier),
			})
		}
	}

	// Finalization only required for transfer transactions.
	if tokenTransaction.GetTransferInput() != nil {
		// Recover secrets for each leaf using the collected keyshares
		leafRecoveredSecrets := make([][]byte, len(tokenTransaction.GetTransferInput().GetLeavesToSpend()))
		for i, leafKeyshares := range leafRevocationKeyshares {
			// Validate we have enough shares
			if len(leafKeyshares) < int(startResponse.KeyshareInfo.Threshold) {
				return nil, fmt.Errorf("insufficient keyshares for leaf %d: got %d, need %d",
					i, len(leafKeyshares), startResponse.KeyshareInfo.Threshold)
			}

			// Check for duplicate operator indices
			seenIndices := make(map[uint64]bool)
			for _, keyshare := range leafKeyshares {
				if seenIndices[keyshare.Index] {
					return nil, fmt.Errorf("duplicate operator index %d for leaf %d", keyshare.Index, i)
				}
				seenIndices[keyshare.Index] = true
			}

			shares := make([]*secretsharing.SecretShare, len(leafKeyshares))
			for j, keyshareWithOperatorIndex := range leafKeyshares {
				shares[j] = &secretsharing.SecretShare{
					FieldModulus: secp256k1.S256().N,
					Threshold:    int(startResponse.KeyshareInfo.Threshold),
					Index:        big.NewInt(int64(keyshareWithOperatorIndex.Index)),
					Share:        new(big.Int).SetBytes(keyshareWithOperatorIndex.Keyshare),
				}
			}
			recoveredKey, err := secretsharing.RecoverSecret(shares)
			if err != nil {
				return nil, fmt.Errorf("failed to recover keyshare for leaf %d: %w", i, err)
			}
			leafRecoveredSecrets[i] = recoveredKey.Bytes()
		}
		err = utils.ValidateRevocationKeys(leafRecoveredSecrets, leafToSpendRevocationPublicKeys)
		if err != nil {
			return nil, fmt.Errorf("invalid revocation keys: %w", err)
		}

		// Finalize the token transaction with each operator.
		for _, operator := range config.SigningOperators {
			operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
			if err != nil {
				log.Printf("Error while establishing gRPC connection to operator at %s: %v", operator.Address, err)
				return nil, err
			}
			defer operatorConn.Close()

			operatorClient := pb.NewSparkServiceClient(operatorConn)
			_, err = operatorClient.FinalizeTokenTransaction(ctx, &pb.FinalizeTokenTransactionRequest{
				FinalTokenTransaction:     startResponse.FinalTokenTransaction,
				LeafToSpendRevocationKeys: leafRecoveredSecrets,
			})
			if err != nil {
				log.Printf("Error while finalizing token transaction with operator %s: %v", operator.Identifier, err)
				return nil, err
			}
		}
	}

	return startResponse.FinalTokenTransaction, nil
}

// FreezeTokens sends a request to freeze all tokens owned by a specific owner public key.
// This prevents transfer of all leaves owned now and in the future by the provided owner public key.
func FreezeTokens(
	ctx context.Context,
	config *Config,
	ownerPublicKey []byte,
	tokenPublicKey []byte,
	shouldUnfreeze bool,
) (*pb.FreezeTokensResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoodinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	var lastResponse *pb.FreezeTokensResponse
	timestamp := uint64(time.Now().UnixNano())
	for _, operator := range config.SigningOperators {
		// Establish connection to coordinator
		sparkConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", operator.Address, err)
			return nil, err
		}
		defer sparkConn.Close()

		// Authenticate
		token, err := AuthenticateWithConnection(ctx, config, sparkConn)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate with server: %v", err)
		}
		tmpCtx := ContextWithToken(ctx, token)
		sparkClient := pb.NewSparkServiceClient(sparkConn)

		// Create the freeze tokens payload
		payload := &pb.FreezeTokensPayload{
			OwnerPublicKey:            ownerPublicKey,
			TokenPublicKey:            tokenPublicKey,
			OperatorIdentityPublicKey: operator.IdentityPublicKey,
			Timestamp:                 timestamp,
			ShouldUnfreeze:            shouldUnfreeze,
		}

		// Hash and sign the payload with the issuer's private key
		payloadHash, err := utils.HashFreezeTokensPayload(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to hash freeze tokens payload: %v", err)
		}

		// Sign with the issuer's private key
		signingPrivKeySecp := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize())
		issuerSignature := ecdsa.Sign(signingPrivKeySecp, payloadHash).Serialize()

		// Create and send the freeze request
		request := &pb.FreezeTokensRequest{
			FreezeTokensPayload: payload,
			IssuerSignature:     issuerSignature,
		}

		lastResponse, err = sparkClient.FreezeTokens(tmpCtx, request)
		if err != nil {
			return nil, fmt.Errorf("failed to freeze tokens: %v", err)
		}
	}
	return lastResponse, nil
}

func GetOwnedTokenLeaves(
	ctx context.Context,
	config *Config,
	ownerPublicKey []byte,
	tokenPublicKey []byte,
) (*pb.GetOwnedTokenLeavesResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
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

	request := &pb.GetOwnedTokenLeavesRequest{
		OwnerPublicKey: ownerPublicKey,
		TokenPublicKey: tokenPublicKey,
	}

	response, err := sparkClient.GetOwnedTokenLeaves(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get owned token leaves: %v", err)
	}
	return response, nil
}

func parseHexIdentifierToUint64(binaryIdentifier string) uint64 {
	value, _ := strconv.ParseUint(binaryIdentifier, 16, 64)
	return value
}
