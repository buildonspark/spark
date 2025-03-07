package wallet

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/utils"
)

// KeyshareWithOperatorIndex holds a keyshare and its corresponding operator index.
type KeyshareWithOperatorIndex struct {
	Keyshare []byte
	Index    uint64
}

// StartTokenTransaction requests the coordinator to build the final token transaction and
// returns the StartTokenTransactionResponse. This includes filling the revocation public keys
// for outputs, adding leaf ids and withdrawal params, and returning keyshare configuration.
func StartTokenTransaction(
	ctx context.Context,
	config *Config,
	tokenTransaction *pb.TokenTransaction,
	leafToSpendPrivateKeys []*secp256k1.PrivateKey,
	leafToSpendRevocationPublicKeys [][]byte,
) (*pb.StartTokenTransactionResponse, []byte, []byte, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoodinatorAddress(), err)
		return nil, nil, nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	// Attach operator public keys to the transaction
	var operatorKeys [][]byte
	for _, operator := range config.SigningOperators {
		operatorKeys = append(operatorKeys, operator.IdentityPublicKey)
	}
	tokenTransaction.SparkOperatorIdentityPublicKeys = operatorKeys

	// Hash the partial token transaction
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		log.Printf("Error while hashing partial token transaction: %v", err)
		return nil, nil, nil, err
	}

	// Gather owner (issuer or leaf) signatures
	var ownerSignatures [][]byte
	if tokenTransaction.GetMintInput() != nil {
		signingPrivKeySecp := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize())
		sig, err := createTokenTransactionSignature(config, signingPrivKeySecp, partialTokenTransactionHash)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create signature: %v", err)
		}
		ownerSignatures = append(ownerSignatures, sig)
	} else if tokenTransaction.GetTransferInput() != nil {
		for i := range leafToSpendPrivateKeys {
			sig, err := createTokenTransactionSignature(config, leafToSpendPrivateKeys[i], partialTokenTransactionHash)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to create signature: %v", err)
			}
			ownerSignatures = append(ownerSignatures, sig)
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
		return nil, nil, nil, err
	}

	// Validate the keyshare config matches our signing operators
	if len(startResponse.KeyshareInfo.OwnerIdentifiers) != len(config.SigningOperators) {
		return nil, nil, nil, fmt.Errorf(
			"keyshare operator count (%d) does not match signing operator count (%d)",
			len(startResponse.KeyshareInfo.OwnerIdentifiers),
			len(config.SigningOperators),
		)
	}
	for _, operatorID := range startResponse.KeyshareInfo.OwnerIdentifiers {
		if _, exists := config.SigningOperators[operatorID]; !exists {
			return nil, nil, nil, fmt.Errorf("keyshare operator %s not found in signing operator list", operatorID)
		}
	}

	// Return the hashed partial, the newly built final transaction, and the start response
	finalTxHash, err := utils.HashTokenTransaction(startResponse.FinalTokenTransaction, false)
	if err != nil {
		log.Printf("Error while hashing final token transaction: %v", err)
		return nil, nil, nil, err
	}

	return startResponse, partialTokenTransactionHash, finalTxHash, nil
}

// SignTokenTransaction calls each signing operator to sign the final token transaction and
// optionally return keyshares (for transfer transactions). It returns a 2D slice of
// KeyshareWithOperatorIndex for each leaf if transfer, or an empty structure if mint.
// If specificOperatorIDs is provided and not empty, only those operators will be contacted.
func SignTokenTransaction(
	ctx context.Context,
	config *Config,
	finalTx *pb.TokenTransaction,
	finalTxHash []byte,
	leafToSpendPrivateKeys []*secp256k1.PrivateKey,
	specificOperatorIDs ...string,
) ([][]*KeyshareWithOperatorIndex, error) {
	// ---------------------------------------------------------------------
	// (A) Build operator-specific signatures
	// ---------------------------------------------------------------------
	var operatorSpecificSignatures []*pb.OperatorSpecificTokenTransactionSignature

	payload := &pb.OperatorSpecificTokenTransactionSignablePayload{
		FinalTokenTransactionHash: finalTxHash,
		OperatorIdentityPublicKey: config.IdentityPublicKey(),
	}
	payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(payload)
	if err != nil {
		log.Printf("Error while hashing operator-specific payload: %v", err)
		return nil, err
	}

	// For mint transactions
	if finalTx.GetMintInput() != nil {
		privKey := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize())
		sig, err := createTokenTransactionSignature(config, privKey, payloadHash)
		if err != nil {
			return nil, fmt.Errorf("failed to create signature: %v", err)
		}
		operatorSpecificSignatures = append(operatorSpecificSignatures, &pb.OperatorSpecificTokenTransactionSignature{
			OwnerPublicKey: config.IdentityPublicKey(),
			OwnerSignature: sig,
			Payload:        payload,
		})
	}

	// For transfer transactions
	if finalTx.GetTransferInput() != nil {
		for i := range finalTx.GetTransferInput().GetLeavesToSpend() {
			sig, err := createTokenTransactionSignature(config, leafToSpendPrivateKeys[i], payloadHash)
			if err != nil {
				return nil, fmt.Errorf("failed to create signature: %v", err)
			}
			operatorSpecificSignatures = append(operatorSpecificSignatures, &pb.OperatorSpecificTokenTransactionSignature{
				OwnerPublicKey: leafToSpendPrivateKeys[i].PubKey().SerializeCompressed(),
				OwnerSignature: sig,
				Payload:        payload,
			})
		}
	}

	// ---------------------------------------------------------------------
	// (B) Contact each operator to sign
	// ---------------------------------------------------------------------
	leafRevocationKeyshares := make([][]*KeyshareWithOperatorIndex, len(finalTx.GetTransferInput().GetLeavesToSpend()))

	// Determine which operators to contact
	operatorsToContact := config.SigningOperators

	// If specific operators are requested, filter the map
	if len(specificOperatorIDs) > 0 {
		operatorsToContact = make(map[string]*so.SigningOperator)
		for _, opID := range specificOperatorIDs {
			if operator, exists := config.SigningOperators[opID]; exists {
				operatorsToContact[opID] = operator
			} else {
				return nil, fmt.Errorf("specified operator ID %s not found in signing operators", opID)
			}
		}
	}

	for _, operator := range operatorsToContact {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to operator at %s: %v", operator.Address, err)
			return nil, err
		}
		defer operatorConn.Close()

		operatorClient := pb.NewSparkServiceClient(operatorConn)
		signTokenTransactionResponse, err := operatorClient.SignTokenTransaction(ctx, &pb.SignTokenTransactionRequest{
			FinalTokenTransaction:      finalTx,
			OperatorSpecificSignatures: operatorSpecificSignatures,
			IdentityPublicKey:          config.IdentityPublicKey(),
		})
		if err != nil {
			log.Printf("Error while calling SignTokenTransaction with operator %s: %v", operator.Identifier, err)
			return nil, err
		}

		// Validate signature
		operatorSig := signTokenTransactionResponse.SparkOperatorSignature
		if err := utils.ValidateOwnershipSignature(operatorSig, finalTxHash, operator.IdentityPublicKey); err != nil {
			return nil, fmt.Errorf("invalid signature from operator with public key %x: %v", operator.IdentityPublicKey, err)
		}

		// Store leaf keyshares if transfer
		for leafIndex, keyshare := range signTokenTransactionResponse.TokenTransactionRevocationKeyshares {
			leafRevocationKeyshares[leafIndex] = append(
				leafRevocationKeyshares[leafIndex],
				&KeyshareWithOperatorIndex{
					Keyshare: keyshare,
					Index:    parseHexIdentifierToUint64(operator.Identifier),
				},
			)
		}
	}

	return leafRevocationKeyshares, nil
}

// FinalizeTokenTransaction handles the final step for transfer transactions, using the recovered
// revocation keys to finalize the transaction with each operator.
func FinalizeTokenTransaction(
	ctx context.Context,
	config *Config,
	finalTx *pb.TokenTransaction,
	leafRevocationKeyshares [][]*KeyshareWithOperatorIndex,
	leafToSpendRevocationPublicKeys [][]byte,
	startResponse *pb.StartTokenTransactionResponse,
) error {
	// Recover secrets from keyshares
	leafRecoveredSecrets := make([][]byte, len(finalTx.GetTransferInput().GetLeavesToSpend()))
	for i, leafKeyshares := range leafRevocationKeyshares {
		// Ensure we have enough shares
		if len(leafKeyshares) < int(startResponse.KeyshareInfo.Threshold) {
			return fmt.Errorf(
				"insufficient keyshares for leaf %d: got %d, need %d",
				i, len(leafKeyshares), startResponse.KeyshareInfo.Threshold,
			)
		}
		seenIndices := make(map[uint64]bool)
		for _, keyshare := range leafKeyshares {
			if seenIndices[keyshare.Index] {
				return fmt.Errorf("duplicate operator index %d for leaf %d", keyshare.Index, i)
			}
			seenIndices[keyshare.Index] = true
		}
		shares := make([]*secretsharing.SecretShare, len(leafKeyshares))
		for j, keyshareWithIndex := range leafKeyshares {
			shares[j] = &secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(startResponse.KeyshareInfo.Threshold),
				Index:        big.NewInt(int64(keyshareWithIndex.Index)),
				Share:        new(big.Int).SetBytes(keyshareWithIndex.Keyshare),
			}
		}
		recoveredKey, err := secretsharing.RecoverSecret(shares)
		if err != nil {
			return fmt.Errorf("failed to recover keyshare for leaf %d: %w", i, err)
		}
		leafRecoveredSecrets[i] = recoveredKey.Bytes()
	}

	// Validate revocation keys
	if err := utils.ValidateRevocationKeys(leafRecoveredSecrets, leafToSpendRevocationPublicKeys); err != nil {
		return fmt.Errorf("invalid revocation keys: %w", err)
	}

	// For each operator, finalize the transaction
	for _, operator := range config.SigningOperators {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to operator at %s: %v", operator.Address, err)
			return err
		}
		defer operatorConn.Close()

		operatorClient := pb.NewSparkServiceClient(operatorConn)
		_, err = operatorClient.FinalizeTokenTransaction(ctx, &pb.FinalizeTokenTransactionRequest{
			FinalTokenTransaction:     startResponse.FinalTokenTransaction,
			LeafToSpendRevocationKeys: leafRecoveredSecrets,
			IdentityPublicKey:         config.IdentityPublicKey(),
		})
		if err != nil {
			log.Printf("Error while finalizing token transaction with operator %s: %v", operator.Identifier, err)
			return err
		}
	}

	return nil
}

// BroadcastTokenTransaction orchestrates all three steps: StartTokenTransaction, SignTokenTransaction,
// and FinalizeTokenTransaction. It returns the finalized token transaction.
func BroadcastTokenTransaction(
	ctx context.Context,
	config *Config,
	tokenTransaction *pb.TokenTransaction,
	leafToSpendPrivateKeys []*secp256k1.PrivateKey,
	leafToSpendRevocationPublicKeys [][]byte,
) (*pb.TokenTransaction, error) {
	// 1) Start token transaction
	startResp, _, finalTxHash, err := StartTokenTransaction(
		ctx,
		config,
		tokenTransaction,
		leafToSpendPrivateKeys,
		leafToSpendRevocationPublicKeys,
	)
	if err != nil {
		return nil, err
	}

	// 2) Sign token transaction
	leafRevocationKeyshares, err := SignTokenTransaction(
		ctx,
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		leafToSpendPrivateKeys,
	)
	if err != nil {
		return nil, err
	}

	// 3) If transfer, finalize
	if tokenTransaction.GetTransferInput() != nil {
		err = FinalizeTokenTransaction(
			ctx,
			config,
			startResp.FinalTokenTransaction,
			leafRevocationKeyshares,
			leafToSpendRevocationPublicKeys,
			startResp,
		)
		if err != nil {
			return nil, err
		}
	}

	return startResp.FinalTokenTransaction, nil
}

// FreezeTokens sends a request to freeze (or unfreeze) all tokens owned by a specific owner public key.
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
	timestamp := uint64(time.Now().UnixMilli())
	for _, operator := range config.SigningOperators {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", operator.Address, err)
			return nil, err
		}
		defer operatorConn.Close()

		token, err := AuthenticateWithConnection(ctx, config, operatorConn)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate with server: %v", err)
		}
		tmpCtx := ContextWithToken(ctx, token)
		sparkClient := pb.NewSparkServiceClient(operatorConn)

		payload := &pb.FreezeTokensPayload{
			OwnerPublicKey:            ownerPublicKey,
			TokenPublicKey:            tokenPublicKey,
			OperatorIdentityPublicKey: operator.IdentityPublicKey,
			IssuerProvidedTimestamp:   timestamp,
			ShouldUnfreeze:            shouldUnfreeze,
		}

		payloadHash, err := utils.HashFreezeTokensPayload(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to hash freeze tokens payload: %v", err)
		}

		signingPrivKeySecp := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize())
		sig, err := createTokenTransactionSignature(config, signingPrivKeySecp, payloadHash)
		if err != nil {
			return nil, fmt.Errorf("failed to create signature: %v", err)
		}
		issuerSignature := sig

		request := &pb.FreezeTokensRequest{
			FreezeTokensPayload: payload,
			IssuerSignature:     issuerSignature,
		}

		lastResponse, err = sparkClient.FreezeTokens(tmpCtx, request)
		if err != nil {
			return nil, fmt.Errorf("failed to freeze/unfreeze tokens: %v", err)
		}
	}
	return lastResponse, nil
}

// GetOwnedTokenLeaves retrieves the leaves for a given set of owner and token public keys.
func GetOwnedTokenLeaves(
	ctx context.Context,
	config *Config,
	ownerPublicKeys [][]byte,
	tokenPublicKeys [][]byte,
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
		OwnerPublicKeys: ownerPublicKeys,
		TokenPublicKeys: tokenPublicKeys,
	}

	response, err := sparkClient.GetOwnedTokenLeaves(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get owned token leaves: %v", err)
	}
	return response, nil
}

// QueryTokenTransactions queries token transactions with optional filters and pagination.
func QueryTokenTransactions(
	ctx context.Context,
	config *Config,
	tokenPublicKeys [][]byte,
	ownerPublicKeys [][]byte,
	leafIDs []string,
	transactionHashes [][]byte,
	offset int64,
	limit int64,
) (*pb.QueryTokenTransactionsResponse, error) {
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

	request := &pb.QueryTokenTransactionsRequest{
		OwnerPublicKeys:        ownerPublicKeys,
		TokenPublicKeys:        tokenPublicKeys,
		LeafIds:                leafIDs,
		TokenTransactionHashes: transactionHashes,
		Limit:                  limit,
		Offset:                 offset,
	}

	response, err := sparkClient.QueryTokenTransactions(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to query token transactions: %v", err)
	}

	return response, nil
}

// CancelTokenTransaction cancels a token transaction that has been signed but not yet finalized.
// This is only possible if fewer than (total operators - threshold) operators have signed the transaction.
// If specificOperatorIDs is provided and not empty, only those operators will be contacted.
func CancelTokenTransaction(
	ctx context.Context,
	config *Config,
	finalTokenTransaction *pb.TokenTransaction,
	specificOperatorIDs ...string,
) error {
	// Determine which operators to contact
	operatorsToContact := config.SigningOperators

	// If specific operators are requested, filter the map
	if len(specificOperatorIDs) > 0 {
		operatorsToContact = make(map[string]*so.SigningOperator)
		for _, opID := range specificOperatorIDs {
			if operator, exists := config.SigningOperators[opID]; exists {
				operatorsToContact[opID] = operator
			} else {
				return fmt.Errorf("specified operator ID %s not found in signing operators", opID)
			}
		}
	}

	// Now cancel with each operator
	for _, operator := range operatorsToContact {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to operator at %s: %v", operator.Address, err)
			return err
		}
		defer operatorConn.Close()

		operatorToken, err := AuthenticateWithConnection(ctx, config, operatorConn)
		if err != nil {
			return fmt.Errorf("failed to authenticate with operator %s: %v", operator.Identifier, err)
		}
		operatorCtx := ContextWithToken(ctx, operatorToken)
		operatorClient := pb.NewSparkServiceClient(operatorConn)

		_, err = operatorClient.CancelSignedTokenTransaction(operatorCtx, &pb.CancelSignedTokenTransactionRequest{
			FinalTokenTransaction:   finalTokenTransaction,
			SenderIdentityPublicKey: config.IdentityPublicKey(),
		})
		if err != nil {
			return fmt.Errorf("failed to cancel token transaction with operator %s: %v", operator.Identifier, err)
		}
	}

	return nil
}

func parseHexIdentifierToUint64(binaryIdentifier string) uint64 {
	value, _ := strconv.ParseUint(binaryIdentifier, 16, 64)
	return value
}

// Helper function to create either Schnorr or ECDSA signature
func createTokenTransactionSignature(config *Config, privKey *secp256k1.PrivateKey, hash []byte) ([]byte, error) {
	if config.UseTokenTransactionSchnorrSignatures {
		sig, err := schnorr.Sign(privKey, hash)
		if err != nil {
			return nil, fmt.Errorf("failed to create Schnorr signature: %v", err)
		}
		return sig.Serialize(), nil
	}

	sig := ecdsa.Sign(privKey, hash)
	return sig.Serialize(), nil
}
