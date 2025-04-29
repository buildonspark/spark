package wallet

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/utils"
)

// KeyshareWithOperatorIndex holds a keyshare and its corresponding operator index.
type KeyshareWithOperatorIndex struct {
	Keyshare      *pb.KeyshareWithIndex
	OperatorIndex uint64
}

// OperatorSignatures maps operator identifiers to their signatures returned as part of the SignTokenTransaction() call.
type OperatorSignatures map[string][]byte

type SerializedPublicKey []byte

// StartTokenTransaction requests the coordinator to build the final token transaction and
// returns the StartTokenTransactionResponse. This includes filling the revocation public keys
// for outputs, adding output ids and withdrawal params, and returning keyshare configuration.
func StartTokenTransaction(
	ctx context.Context,
	config *Config,
	tokenTransaction *pb.TokenTransaction,
	ownerPrivateKeys []*secp256k1.PrivateKey,
	startSignatureIndexOrder []uint32,
) (*pb.StartTokenTransactionResponse, []byte, []byte, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
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

	// Gather owner (issuer or output) signatures
	var ownerSignaturesWithIndex []*pb.SignatureWithIndex
	if tokenTransaction.GetMintInput() != nil {
		signingPrivKeySecp := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey.Serialize())
		sig, err := createTokenTransactionSignature(config, signingPrivKeySecp, partialTokenTransactionHash)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create signature: %v", err)
		}
		sigWithIndex := &pb.SignatureWithIndex{
			InputIndex: 0,
			Signature:  sig,
		}
		ownerSignaturesWithIndex = append(ownerSignaturesWithIndex, sigWithIndex)
	} else if tokenTransaction.GetTransferInput() != nil {
		signaturesByIndex := make(map[uint32]*pb.SignatureWithIndex)

		// If startSignatureIndexOrder is provided and has the correct length, use it to order signatures
		if len(startSignatureIndexOrder) > 0 && len(startSignatureIndexOrder) != len(ownerPrivateKeys) {
			return nil, nil, nil, fmt.Errorf("startSignatureIndexOrder length (%d) does not match ownerPrivateKeys length (%d)",
				len(startSignatureIndexOrder), len(ownerPrivateKeys))
		}
		for i, privKey := range ownerPrivateKeys {
			sig, err := createTokenTransactionSignature(config, privKey, partialTokenTransactionHash)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to create signature: %v", err)
			}
			sigWithIndex := &pb.SignatureWithIndex{
				InputIndex: uint32(i),
				Signature:  sig,
			}
			signaturesByIndex[uint32(i)] = sigWithIndex
		}

		// If using custom order, ensure we have all required indices
		if len(startSignatureIndexOrder) > 0 {
			for _, idx := range startSignatureIndexOrder {
				if _, exists := signaturesByIndex[idx]; !exists {
					return nil, nil, nil, fmt.Errorf("missing signature for required input index %d", idx)
				}
			}
		}

		// If signatureOrder is provided, use it to determine position in the array
		if len(startSignatureIndexOrder) > 0 {
			for _, idx := range startSignatureIndexOrder {
				ownerSignaturesWithIndex = append(ownerSignaturesWithIndex, signaturesByIndex[idx])
			}
		} else {
			for i := range ownerPrivateKeys {
				ownerSignaturesWithIndex = append(ownerSignaturesWithIndex, signaturesByIndex[uint32(i)])
			}
		}
	}

	startResponse, err := sparkClient.StartTokenTransaction(tmpCtx, &pb.StartTokenTransactionRequest{
		IdentityPublicKey:       config.IdentityPublicKey(),
		PartialTokenTransaction: tokenTransaction,
		TokenTransactionSignatures: &pb.TokenTransactionSignatures{
			OwnerSignatures: ownerSignaturesWithIndex,
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

// createOperatorSpecificSignature creates a signature for the operator-specific payload
// using the provided private key and returns the OperatorSpecificTokenTransactionSignature.
func createOperatorSpecificSignature(
	config *Config,
	operatorPublicKey SerializedPublicKey,
	privKey *secp256k1.PrivateKey,
	inputIndex uint32,
	finalTxHash []byte,
) (*pb.OperatorSpecificOwnerSignature, error) {
	payload := &pb.OperatorSpecificTokenTransactionSignablePayload{
		FinalTokenTransactionHash: finalTxHash,
		OperatorIdentityPublicKey: operatorPublicKey,
	}
	payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(payload)
	if err != nil {
		return nil, fmt.Errorf("error while hashing operator-specific payload: %v", err)
	}

	sig, err := createTokenTransactionSignature(config, privKey, payloadHash)
	if err != nil {
		return nil, fmt.Errorf("failed to create signature: %v", err)
	}

	return &pb.OperatorSpecificOwnerSignature{
		OwnerSignature: &pb.SignatureWithIndex{
			InputIndex: inputIndex,
			Signature:  sig,
		},
		Payload: payload,
	}, nil
}

// getOperatorsToContact determines which operators to contact based on provided public keys.
// If operatorIdentityPublicKeys is empty, all signing operators will be used.
func getOperatorsToContact(
	config *Config,
	operatorIdentityPublicKeys []SerializedPublicKey,
) ([]*so.SigningOperator, []SerializedPublicKey, error) {
	var operatorsToContact []*so.SigningOperator
	var selectedPubKeys []SerializedPublicKey

	if len(operatorIdentityPublicKeys) > 0 {
		for _, opPubKey := range operatorIdentityPublicKeys {
			// Find the operator with this public key
			found := false
			for _, operator := range config.SigningOperators {
				if bytes.Equal(operator.IdentityPublicKey, opPubKey) {
					operatorsToContact = append(operatorsToContact, operator)
					selectedPubKeys = append(selectedPubKeys, opPubKey)
					found = true
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("operator with public key %x not found in signing operators", opPubKey)
			}
		}
	} else {
		// Use all signing operators
		for _, operator := range config.SigningOperators {
			operatorsToContact = append(operatorsToContact, operator)
			selectedPubKeys = append(selectedPubKeys, operator.IdentityPublicKey)
		}
	}

	return operatorsToContact, selectedPubKeys, nil
}

// SignTokenTransaction calls each signing operator to sign the final token transaction and
// optionally return keyshares (for transfer transactions). It returns a 2D slice of
// KeyshareWithOperatorIndex for each output if transfer, or an empty structure if mint.
// If operatorIdentityPublicKeys is provided and not empty, only those operators will be contacted,
// otherwise all operators will be used.
func SignTokenTransaction(
	ctx context.Context,
	config *Config,
	finalTx *pb.TokenTransaction,
	finalTxHash []byte,
	operatorIdentityPublicKeys []SerializedPublicKey,
	ownerPrivateKeys []*secp256k1.PrivateKey,
	signatureOrder []uint32,
) ([][]*KeyshareWithOperatorIndex, OperatorSignatures, error) {
	operatorSignaturesMap := make(OperatorSignatures)
	outputRevocationKeyshares := make([][]*KeyshareWithOperatorIndex, len(finalTx.GetTransferInput().GetOutputsToSpend()))

	operatorsToContact, selectedPubKeys, err := getOperatorsToContact(config, operatorIdentityPublicKeys)
	if err != nil {
		return nil, nil, err
	}

	// Validate signatureOrder if provided
	if len(signatureOrder) > 0 && len(signatureOrder) != len(ownerPrivateKeys) {
		return nil, nil, fmt.Errorf("signatureOrder length (%d) does not match ownerPrivateKeys length (%d)",
			len(signatureOrder), len(ownerPrivateKeys))
	}

	for operatorIndex, operator := range operatorsToContact {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address, nil)
		if err != nil {
			log.Printf("Error while establishing gRPC connection to operator at %s: %v", operator.Address, err)
			return nil, nil, err
		}
		defer operatorConn.Close()

		operatorToken, err := AuthenticateWithConnection(ctx, config, operatorConn)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to authenticate with operator %s: %v", operator.Identifier, err)
		}
		operatorCtx := ContextWithToken(ctx, operatorToken)
		operatorClient := pb.NewSparkServiceClient(operatorConn)

		operatorSpecificSignatures := make([]*pb.OperatorSpecificOwnerSignature, len(ownerPrivateKeys))
		for i, privKey := range ownerPrivateKeys {
			inputIndex := uint32(i)

			sig, err := createOperatorSpecificSignature(
				config,
				selectedPubKeys[operatorIndex],
				privKey,
				inputIndex,
				finalTxHash,
			)
			if err != nil {
				return nil, nil, err
			}

			// If signatureOrder is provided, use it to determine position in the array
			if len(signatureOrder) > 0 {
				operatorSpecificSignatures[signatureOrder[i]] = sig
			} else {
				operatorSpecificSignatures[i] = sig
			}
		}

		signTokenTransactionResponse, err := operatorClient.SignTokenTransaction(operatorCtx, &pb.SignTokenTransactionRequest{
			FinalTokenTransaction:      finalTx,
			OperatorSpecificSignatures: operatorSpecificSignatures,
			IdentityPublicKey:          config.IdentityPublicKey(),
		})
		if err != nil {
			log.Printf("Error while calling SignTokenTransaction with operator %s: %v", operator.Identifier, err)
			return nil, nil, err
		}
		// Validate signature
		operatorSig := signTokenTransactionResponse.SparkOperatorSignature
		if err := utils.ValidateOwnershipSignature(operatorSig, finalTxHash, operator.IdentityPublicKey); err != nil {
			return nil, nil, fmt.Errorf("invalid signature from operator with public key %x: %v", operator.IdentityPublicKey, err)
		}

		// Store output keyshares if transfer
		for _, keyshare := range signTokenTransactionResponse.RevocationKeyshares {
			outputRevocationKeyshares[keyshare.InputIndex] = append(
				outputRevocationKeyshares[keyshare.InputIndex],
				&KeyshareWithOperatorIndex{
					Keyshare:      keyshare,
					OperatorIndex: parseHexIdentifierToUint64(operator.Identifier),
				},
			)
		}
		operatorSignaturesMap[operator.Identifier] = operatorSig
	}

	// Validate that we have enough keyshares for each output and no duplicates
	for i, outputKeyshares := range outputRevocationKeyshares {
		if len(outputKeyshares) < len(finalTx.GetTransferInput().GetOutputsToSpend()) {
			// Determine which keyshares are missing
			expectedInputs := int(config.Threshold)
			presentIndices := make(map[uint64]bool)
			for _, keyshare := range outputKeyshares {
				presentIndices[keyshare.OperatorIndex] = true
			}

			var missingIndices []string
			for j := 0; j < expectedInputs; j++ {
				if !presentIndices[uint64(j)] {
					missingIndices = append(missingIndices, fmt.Sprintf("%d", j))
				}
			}

			return nil, nil, fmt.Errorf(
				"insufficient keyshares for output %d: got %d, need %d (missing indices: %s)",
				i, len(outputKeyshares), config.Threshold,
				strings.Join(missingIndices, ", "),
			)
		}

		seenIndices := make(map[uint64]bool)
		for _, keyshare := range outputKeyshares {
			if seenIndices[keyshare.OperatorIndex] {
				return nil, nil, fmt.Errorf("duplicate operator index %d for output %d", keyshare.OperatorIndex, i)
			}
			seenIndices[keyshare.OperatorIndex] = true
		}
	}

	return outputRevocationKeyshares, operatorSignaturesMap, nil
}

// FinalizeTokenTransaction handles the final step for transfer transactions, using the recovered
// revocation keys to finalize the transaction with each operator.
func FinalizeTokenTransaction(
	ctx context.Context,
	config *Config,
	finalTx *pb.TokenTransaction,
	outputRevocationKeyshares [][]*KeyshareWithOperatorIndex,
	outputToSpendRevocationCommitments []SerializedPublicKey,
) error {
	// Recover secrets from keyshares
	outputRecoveredSecrets := make([]*secp256k1.PrivateKey, len(finalTx.GetTransferInput().GetOutputsToSpend()))
	for i, outputKeyshares := range outputRevocationKeyshares {
		shares := make([]*secretsharing.SecretShare, len(outputKeyshares))
		for j, keyshareWithOperatorIndex := range outputKeyshares {
			shares[j] = &secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(config.Threshold),
				Index:        big.NewInt(int64(keyshareWithOperatorIndex.OperatorIndex)),
				Share:        new(big.Int).SetBytes(keyshareWithOperatorIndex.Keyshare.Keyshare),
			}
		}
		recoveredKey, err := secretsharing.RecoverSecret(shares)
		if err != nil {
			return fmt.Errorf("failed to recover keyshare for output %d: %w", i, err)
		}

		privKey, err := common.PrivateKeyFromBigInt(recoveredKey)
		if err != nil {
			return fmt.Errorf("failed to convert recovered keyshare to private key for output %d: %w", i, err)
		}

		outputRecoveredSecrets[i] = privKey
	}

	// Validate revocation keys
	if err := utils.ValidateRevocationKeys(outputRecoveredSecrets, toByteSlices(outputToSpendRevocationCommitments)); err != nil {
		return fmt.Errorf("invalid revocation keys: %w", err)
	}

	revocationSecrets := make([]*pb.RevocationSecretWithIndex, len(outputRecoveredSecrets))
	for i, privKey := range outputRecoveredSecrets {
		revocationSecrets[i] = &pb.RevocationSecretWithIndex{
			InputIndex:       uint32(i),
			RevocationSecret: privKey.Serialize(),
		}
	}

	// For each operator, finalize the transaction
	for _, operator := range config.SigningOperators {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address, nil)
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

		_, err = operatorClient.FinalizeTokenTransaction(operatorCtx, &pb.FinalizeTokenTransactionRequest{
			FinalTokenTransaction: finalTx,
			RevocationSecrets:     revocationSecrets,
			IdentityPublicKey:     config.IdentityPublicKey(),
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
	ownerPrivateKeys []*secp256k1.PrivateKey,
	outputToSpendRevocationCommitments []SerializedPublicKey,
) (*pb.TokenTransaction, error) {
	// 1) Start token transaction
	startResp, _, finalTxHash, err := StartTokenTransaction(
		ctx,
		config,
		tokenTransaction,
		ownerPrivateKeys,
		nil,
	)
	if err != nil {
		return nil, err
	}
	// 2) Sign token transaction
	outputRevocationKeyshares, _, err := SignTokenTransaction(
		ctx,
		config,
		startResp.FinalTokenTransaction,
		finalTxHash,
		nil, // Specify nil to designate that all operators should be contacted.
		ownerPrivateKeys,
		nil,
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
			outputRevocationKeyshares,
			outputToSpendRevocationCommitments,
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
	ownerPublicKey SerializedPublicKey,
	tokenPublicKey SerializedPublicKey,
	shouldUnfreeze bool,
) (*pb.FreezeTokensResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoodinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	var lastResponse *pb.FreezeTokensResponse
	timestamp := uint64(time.Now().UnixMilli())
	for _, operator := range config.SigningOperators {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address, nil)
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

// QueryTokenOutputs retrieves the token outputs for a given set of owner and token public keys.
func QueryTokenOutputs(
	ctx context.Context,
	config *Config,
	ownerPublicKeys []SerializedPublicKey,
	tokenPublicKeys []SerializedPublicKey,
) (*pb.QueryTokenOutputsResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
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

	request := &pb.QueryTokenOutputsRequest{
		OwnerPublicKeys: toByteSlices(ownerPublicKeys),
		TokenPublicKeys: toByteSlices(tokenPublicKeys),
	}

	response, err := sparkClient.QueryTokenOutputs(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get token outputs: %v", err)
	}
	return response, nil
}

// QueryTokenTransactions queries token transactions with optional filters and pagination.
func QueryTokenTransactions(
	ctx context.Context,
	config *Config,
	tokenPublicKeys []SerializedPublicKey,
	ownerPublicKeys []SerializedPublicKey,
	outputIDs []string,
	transactionHashes [][]byte,
	offset int64,
	limit int64,
) (*pb.QueryTokenTransactionsResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
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
		OwnerPublicKeys:        toByteSlices(ownerPublicKeys),
		TokenPublicKeys:        toByteSlices(tokenPublicKeys),
		OutputIds:              outputIDs,
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
// If operatorIdentityPublicKeys is provided and not empty, only those operators will be contacted.
func CancelTokenTransaction(
	ctx context.Context,
	config *Config,
	finalTokenTransaction *pb.TokenTransaction,
	operatorIdentityPublicKeys []SerializedPublicKey,
) error {
	operatorsToContact, _, err := getOperatorsToContact(config, operatorIdentityPublicKeys)
	if err != nil {
		return err
	}

	// Now cancel with each operator
	for _, operator := range operatorsToContact {
		operatorConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address, nil)
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

func toByteSlices(keys []SerializedPublicKey) [][]byte {
	result := make([][]byte, len(keys))
	for i, key := range keys {
		result[i] = key
	}
	return result
}
