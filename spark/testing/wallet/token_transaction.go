package wallet

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/protohash"

	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const DefaultValidityDuration = 180 * time.Second

// StartTokenTransaction calls the start_transaction endpoint on the SparkTokenService.
func StartTokenTransaction(
	ctx context.Context,
	config *TestWalletConfig,
	tokenTransaction *tokenpb.TokenTransaction,
	ownerPrivateKeys []keys.Private,
	validityDuration time.Duration,
	startSignatureIndexOrder []uint32,
) (*tokenpb.StartTransactionResponse, []byte, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	sparkClient := tokenpb.NewSparkTokenServiceClient(sparkConn)

	// Hash the partial token transaction
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		log.Printf("Error while hashing partial token transaction: %v", err)
		return nil, nil, err
	}

	// Gather owner (issuer or output) signatures
	var ownerSignaturesWithIndex []*tokenpb.SignatureWithIndex
	signaturesByIndex := make(map[uint32]*tokenpb.SignatureWithIndex)
	// If startSignatureIndexOrder is provided and has the correct length, use it to order signatures
	if len(startSignatureIndexOrder) > 0 && len(startSignatureIndexOrder) != len(ownerPrivateKeys) {
		return nil, nil, fmt.Errorf("startSignatureIndexOrder length (%d) does not match ownerPrivateKeys length (%d)",
			len(startSignatureIndexOrder), len(ownerPrivateKeys))
	}
	if ownerPrivateKeys == nil {
		txType, err := utils.InferTokenTransactionType(tokenTransaction)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to infer token transaction type: %w", err)
		}
		if txType == utils.TokenTransactionTypeCreate || txType == utils.TokenTransactionTypeMint {
			ownerPrivateKeys = []keys.Private{config.IdentityPrivateKey}
		} else {
			return nil, nil, fmt.Errorf("owner signing keys must be specified for transfer transaction")
		}
	}
	for i, privKey := range ownerPrivateKeys {
		sig, err := SignHashSlice(config, privKey, partialTokenTransactionHash)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create signature: %w", err)
		}
		sigWithIndex := &tokenpb.SignatureWithIndex{
			InputIndex: uint32(i),
			Signature:  sig,
		}
		signaturesByIndex[uint32(i)] = sigWithIndex
	}

	// If using custom order, ensure we have all required indices
	if len(startSignatureIndexOrder) > 0 {
		for _, idx := range startSignatureIndexOrder {
			if _, exists := signaturesByIndex[idx]; !exists {
				return nil, nil, fmt.Errorf("missing signature for required input index %d", idx)
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

	startResponse, err := sparkClient.StartTransaction(tmpCtx, &tokenpb.StartTransactionRequest{
		IdentityPublicKey:                      config.IdentityPublicKey().Serialize(),
		PartialTokenTransaction:                tokenTransaction,
		PartialTokenTransactionOwnerSignatures: ownerSignaturesWithIndex,
		ValidityDurationSeconds:                uint64(validityDuration.Seconds()),
	})
	if err != nil {
		log.Printf("Error while calling StartTokenTransaction: %v", err)
		return nil, nil, err
	}

	// Validate the keyshare config matches our signing operators
	if len(startResponse.KeyshareInfo.OwnerIdentifiers) != len(config.SigningOperators) {
		return nil, nil, fmt.Errorf(
			"keyshare operator count (%d) does not match signing operator count (%d)",
			len(startResponse.KeyshareInfo.OwnerIdentifiers),
			len(config.SigningOperators),
		)
	}
	for _, operatorID := range startResponse.KeyshareInfo.OwnerIdentifiers {
		if _, exists := config.SigningOperators[operatorID]; !exists {
			return nil, nil, fmt.Errorf("keyshare operator %s not found in signing operator list", operatorID)
		}
	}
	finalTxHash, err := utils.HashTokenTransaction(startResponse.FinalTokenTransaction, false)
	if err != nil {
		log.Printf("Error while hashing final token transaction: %v", err)
		return nil, nil, err
	}

	return startResponse, finalTxHash, nil
}

// CommitTransaction calls the commit_transaction endpoint on the SparkTokenService.
func CommitTransaction(
	ctx context.Context,
	config *TestWalletConfig,
	req *tokenpb.CommitTransactionRequest,
	opts ...grpc.CallOption,
) (*tokenpb.CommitTransactionResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()

	client := tokenpb.NewSparkTokenServiceClient(sparkConn)
	operatorToken, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with operator %s: %w", config.CoordinatorIdentifier, err)
	}
	operatorCtx := ContextWithToken(ctx, operatorToken)
	return client.CommitTransaction(operatorCtx, req, opts...)
}

func BroadcastTokenTransfer(
	ctx context.Context,
	config *TestWalletConfig,
	tokenTransaction *tokenpb.TokenTransaction,
	ownerPrivateKeys []keys.Private,
) (*tokenpb.TokenTransaction, error) {
	return BroadcastTokenTransferWithValidityDuration(
		ctx,
		config,
		tokenTransaction,
		DefaultValidityDuration,
		ownerPrivateKeys,
	)
}

// BroadcastTokenTransferWithValidityDuration orchestrates a coordinated token transfer using the new flow:
// 1. StartTokenTransaction - creates the final transaction with revocation commitments
// 2. CommitTransaction - signs and commits the transaction
func BroadcastTokenTransferWithValidityDuration(
	ctx context.Context,
	config *TestWalletConfig,
	tokenTransaction *tokenpb.TokenTransaction,
	validityDuration time.Duration,
	ownerPrivateKeys []keys.Private,
) (*tokenpb.TokenTransaction, error) {
	if tokenTransaction.Version >= 3 {
		return BroadcastTokenTransactionV3(ctx, config, tokenTransaction, ownerPrivateKeys, validityDuration)
	}
	startResp, finalTxHash, err := StartTokenTransaction(
		ctx,
		config,
		tokenTransaction,
		ownerPrivateKeys,
		validityDuration,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start token transaction: %w", err)
	}

	operatorSignatures, err := CreateOperatorSpecificSignatures(config, ownerPrivateKeys, finalTxHash)
	if err != nil {
		return nil, fmt.Errorf("failed to create operator-specific signatures: %w", err)
	}

	signReq := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          startResp.FinalTokenTransaction,
		FinalTokenTransactionHash:      finalTxHash,
		InputTtxoSignaturesPerOperator: operatorSignatures,
		OwnerIdentityPublicKey:         config.IdentityPublicKey().Serialize(),
	}

	_, err = CommitTransaction(ctx, config, signReq)
	if err != nil {
		return nil, fmt.Errorf("failed to sign and commit transaction: %w", err)
	}

	return startResp.FinalTokenTransaction, nil
}

type SignTokenTransactionFromCoordinationParams struct {
	Operator         *so.SigningOperator
	TokenTransaction *tokenpb.TokenTransaction
	FinalTxHash      []byte
	OwnerPrivateKeys []keys.Private
}

// SignTokenTransactionFromCoordination instructs a single operator to sign a token transaction.
// This is normally called by the coordinator to each other SO.
func SignTokenTransactionFromCoordination(
	ctx context.Context,
	config *TestWalletConfig,
	params SignTokenTransactionFromCoordinationParams,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	operatorSignatures, err := CreateOperatorSpecificSignatures(
		config,
		params.OwnerPrivateKeys,
		params.FinalTxHash,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create operator-specific signatures: %w", err)
	}
	var chosenOperatorSignatures *tokenpb.InputTtxoSignaturesPerOperator
	for _, operatorSignatures := range operatorSignatures {
		operatorKey, err := keys.ParsePublicKey(operatorSignatures.OperatorIdentityPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse operator identity public key: %w", err)
		}
		if operatorKey.Equals(params.Operator.IdentityPublicKey) {
			chosenOperatorSignatures = operatorSignatures
			break
		}
	}
	if chosenOperatorSignatures == nil {
		return nil, fmt.Errorf("no signatures found for operator %s: %w", params.Operator.Identifier, err)
	}

	sparkConn, err := params.Operator.NewOperatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()

	client := tokeninternalpb.NewSparkTokenInternalServiceClient(sparkConn)
	return client.SignTokenTransactionFromCoordination(ctx, &tokeninternalpb.SignTokenTransactionFromCoordinationRequest{
		FinalTokenTransaction:          params.TokenTransaction,
		FinalTokenTransactionHash:      params.FinalTxHash,
		InputTtxoSignaturesPerOperator: chosenOperatorSignatures,
		OwnerIdentityPublicKey:         config.IdentityPublicKey().Serialize(),
	})
}

// BroadcastTokenTransactionV3 uses the broadcast_token_handler endpoint to finalize and commit a token transaction.
func BroadcastTokenTransactionV3(
	ctx context.Context,
	config *TestWalletConfig,
	tokenTransaction *tokenpb.TokenTransaction,
	ownerPrivateKeys []keys.Private,
	validityDuration time.Duration,
) (*tokenpb.TokenTransaction, error) {
	req, err := convertTokenTransactionToV3Request(config, tokenTransaction, ownerPrivateKeys, validityDuration)
	if err != nil {
		return nil, err
	}
	return BroadcastTokenTransactionV3Request(ctx, config, req)
}

func BroadcastTokenTransactionV3Request(ctx context.Context,
	config *TestWalletConfig,
	req *tokenpb.BroadcastTransactionRequest,
) (*tokenpb.TokenTransaction, error) {
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}
	ctx = ContextWithToken(ctx, token)

	client := tokenpb.NewSparkTokenServiceClient(conn)
	response, err := client.BroadcastTransaction(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to broadcast token transaction: %w", err)
	}

	finalTx := response.GetFinalTokenTransaction()
	if finalTx == nil {
		return nil, fmt.Errorf("broadcast transaction response missing final transaction")
	}
	legacyTx, err := protoconverter.ConvertFinalToV2TxShape(finalTx)
	if err != nil {
		return nil, fmt.Errorf("failed to convert final token transaction: %w", err)
	}
	return legacyTx, nil
}

// BroadcastTokenTransactionV3WithResponse uses the broadcast_token_handler endpoint and returns the full response.
func BroadcastTokenTransactionV3WithResponse(
	ctx context.Context,
	config *TestWalletConfig,
	tokenTransaction *tokenpb.TokenTransaction,
	ownerPrivateKeys []keys.Private,
	validityDuration time.Duration,
) (*tokenpb.BroadcastTransactionResponse, error) {
	req, err := convertTokenTransactionToV3Request(config, tokenTransaction, ownerPrivateKeys, validityDuration)
	if err != nil {
		return nil, err
	}

	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}
	ctx = ContextWithToken(ctx, token)

	client := tokenpb.NewSparkTokenServiceClient(conn)
	response, err := client.BroadcastTransaction(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to broadcast token transaction: %w", err)
	}

	return response, nil
}

func convertTokenTransactionToV3Request(
	config *TestWalletConfig,
	tokenTransaction *tokenpb.TokenTransaction,
	ownerPrivateKeys []keys.Private,
	validityDuration time.Duration,
) (*tokenpb.BroadcastTransactionRequest, error) {
	if config == nil {
		return nil, fmt.Errorf("wallet config cannot be nil")
	}
	if tokenTransaction == nil {
		return nil, fmt.Errorf("token transaction cannot be nil")
	}
	if validityDuration <= 0 {
		validityDuration = DefaultValidityDuration
	}
	// Set the version to 3 for the broadcast request (to avoid needing to do it upstream in every test)
	tokenTransaction.Version = 3

	if tokenTransaction.ClientCreatedTimestamp == nil || tokenTransaction.ClientCreatedTimestamp.AsTime().IsZero() {
		tokenTransaction.ClientCreatedTimestamp = timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC()))
	}
	tokenTransaction.ValidityDurationSeconds = proto.Uint64(uint64(validityDuration.Seconds()))

	if err := ensureV3WithdrawParameters(config, tokenTransaction); err != nil {
		return nil, err
	}

	signingKeys := ownerPrivateKeys
	if len(signingKeys) == 0 {
		txType, err := utils.InferTokenTransactionType(tokenTransaction)
		if err != nil {
			return nil, fmt.Errorf("failed to infer token transaction type: %w", err)
		}
		if txType == utils.TokenTransactionTypeCreate || txType == utils.TokenTransactionTypeMint {
			signingKeys = []keys.Private{config.IdentityPrivateKey}
		} else {
			return nil, fmt.Errorf("owner signing keys must be specified for transfer transaction")
		}
	}

	partialTx, err := protoconverter.ConvertV2TxShapeToPartial(tokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert legacy token transaction to partial: %w", err)
	}

	// Hash and sign the PartialTokenTransaction (request payload), not the legacy proto.
	partialHash, err := protohash.Hash(partialTx)
	if err != nil {
		return nil, fmt.Errorf("failed to hash partial token transaction: %w", err)
	}

	ownerSignatures := make([]*tokenpb.SignatureWithIndex, 0, len(signingKeys))
	for i, privKey := range signingKeys {
		sig, err := SignHashSlice(config, privKey, partialHash)
		if err != nil {
			return nil, fmt.Errorf("failed to sign token transaction input %d: %w", i, err)
		}
		ownerSignatures = append(ownerSignatures, &tokenpb.SignatureWithIndex{
			InputIndex: uint32(i),
			Signature:  sig,
		})
	}

	identityPublicKey := config.IdentityPublicKey().Serialize()
	if len(identityPublicKey) == 0 {
		return nil, fmt.Errorf("identity public key must not be empty")
	}

	return &tokenpb.BroadcastTransactionRequest{
		IdentityPublicKey:               identityPublicKey,
		PartialTokenTransaction:         partialTx,
		TokenTransactionOwnerSignatures: ownerSignatures,
	}, nil
}

func ensureV3WithdrawParameters(config *TestWalletConfig, tokenTransaction *tokenpb.TokenTransaction) error {
	if config == nil {
		return fmt.Errorf("wallet config cannot be nil")
	}
	if config.WithdrawBondSats == 0 {
		return fmt.Errorf("wallet withdraw bond sats must be configured for v3 transactions")
	}
	if config.WithdrawRelativeBlockLocktime == 0 {
		return fmt.Errorf("wallet withdraw relative block locktime must be configured for v3 transactions")
	}

	if len(tokenTransaction.TokenOutputs) == 0 {
		return nil
	}

	for i, output := range tokenTransaction.TokenOutputs {
		if output == nil {
			continue
		}
		if output.WithdrawBondSats == nil {
			bond := config.WithdrawBondSats
			output.WithdrawBondSats = &bond
		} else if *output.WithdrawBondSats != config.WithdrawBondSats {
			return fmt.Errorf("token output %d withdraw bond sats must equal configured value %d", i, config.WithdrawBondSats)
		}

		if output.WithdrawRelativeBlockLocktime == nil {
			locktime := config.WithdrawRelativeBlockLocktime
			output.WithdrawRelativeBlockLocktime = &locktime
		} else if *output.WithdrawRelativeBlockLocktime != config.WithdrawRelativeBlockLocktime {
			return fmt.Errorf("token output %d withdraw relative block locktime must equal configured value %d", i, config.WithdrawRelativeBlockLocktime)
		}
	}

	return nil
}

// FreezeTokens sends a request to freeze (or unfreeze) all tokens owned by a specific owner public key.
func FreezeTokens(
	ctx context.Context,
	config *TestWalletConfig,
	ownerPublicKey keys.Public,
	tokenIdentifier []byte,
	shouldUnfreeze bool,
) (*tokenpb.FreezeTokensResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	var lastResponse *tokenpb.FreezeTokensResponse
	timestamp := uint64(time.Now().UnixMilli())
	for _, operator := range config.SigningOperators {
		operatorConn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", operator.AddressRpc, err)
			return nil, err
		}
		defer operatorConn.Close()

		token, err := AuthenticateWithConnection(ctx, config, operatorConn)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate with server: %w", err)
		}
		tmpCtx := ContextWithToken(ctx, token)
		sparkTokenClient := tokenpb.NewSparkTokenServiceClient(operatorConn)

		// Must define here to use the hash function that only takes a token prtoo.
		payloadTokenProto := &tokenpb.FreezeTokensPayload{
			Version:                   1,
			OwnerPublicKey:            ownerPublicKey.Serialize(),
			TokenIdentifier:           tokenIdentifier,
			OperatorIdentityPublicKey: operator.IdentityPublicKey.Serialize(),
			IssuerProvidedTimestamp:   timestamp,
			ShouldUnfreeze:            shouldUnfreeze,
		}
		payloadHash, err := utils.HashFreezeTokensPayloadV1(payloadTokenProto)
		if err != nil {
			return nil, fmt.Errorf("failed to hash freeze tokens payload: %w", err)
		}

		sig, err := SignHashSlice(config, config.IdentityPrivateKey, payloadHash)
		if err != nil {
			return nil, fmt.Errorf("failed to create signature: %w", err)
		}
		issuerSignature := sig

		request := &tokenpb.FreezeTokensRequest{
			FreezeTokensPayload: payloadTokenProto,
			IssuerSignature:     issuerSignature,
		}

		lastResponse, err = sparkTokenClient.FreezeTokens(tmpCtx, request)
		if err != nil {
			return nil, fmt.Errorf("failed to freeze/unfreeze tokens: %w", err)
		}
	}
	return lastResponse, nil
}

func CreateOperatorSpecificSignatures(
	config *TestWalletConfig,
	ownerPrivateKeys []keys.Private,
	finalTxHash []byte,
) ([]*tokenpb.InputTtxoSignaturesPerOperator, error) {
	var operatorSignatures []*tokenpb.InputTtxoSignaturesPerOperator

	for _, operator := range config.SigningOperators {
		var ttxoSignatures []*tokenpb.SignatureWithIndex

		for i, privKey := range ownerPrivateKeys {
			payloadHash, err := utils.HashOperatorSpecificPayload(finalTxHash, operator.IdentityPublicKey)
			if err != nil {
				return nil, fmt.Errorf("error while hashing operator-specific payload: %w", err)
			}
			sig, err := SignHashSlice(config, privKey, payloadHash)
			if err != nil {
				return nil, fmt.Errorf("error while creating operator-specific signature: %w", err)
			}

			ttxoSignatures = append(ttxoSignatures, &tokenpb.SignatureWithIndex{
				InputIndex: uint32(i),
				Signature:  sig,
			})
		}

		operatorSignatures = append(operatorSignatures, &tokenpb.InputTtxoSignaturesPerOperator{
			TtxoSignatures:            ttxoSignatures,
			OperatorIdentityPublicKey: operator.IdentityPublicKey.Serialize(),
		})
	}

	return operatorSignatures, nil
}

type ExchangeRevocationSecretsParams struct {
	FinalTokenTransaction *tokenpb.TokenTransaction
	FinalTxHash           []byte
	AllOperatorSignatures map[string][]byte
	RevocationShares      []*tokeninternalpb.OperatorRevocationShares
	TargetOperator        *so.SigningOperator
}

// ExchangeRevocationSecretsManually triggers the revocation secret exchange manually for testing purposes.
// This function allows testing the revocation secret exchange mechanism without going through the full commit process.
func ExchangeRevocationSecretsManually(
	ctx context.Context,
	config *TestWalletConfig,
	exchangeParams ExchangeRevocationSecretsParams,
) error {
	// Prepare the operator signatures package
	allOperatorSignaturesPackage := make([]*tokeninternalpb.OperatorTransactionSignature, 0, len(exchangeParams.AllOperatorSignatures))
	for identifier, sig := range exchangeParams.AllOperatorSignatures {
		operator, exists := config.SigningOperators[identifier]
		if !exists {
			return fmt.Errorf("operator %s not found in signing operators", identifier)
		}
		allOperatorSignaturesPackage = append(allOperatorSignaturesPackage, &tokeninternalpb.OperatorTransactionSignature{
			OperatorIdentityPublicKey: operator.IdentityPublicKey.Serialize(),
			Signature:                 sig,
		})
	}

	entClient, err := ent.Open("postgres", config.CoordinatorDatabaseURI)
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator database: %w", err)
	}
	defer entClient.Close()
	dbCtx := ent.NewContext(ctx, entClient)

	var outputsToSpend []*tokeninternalpb.OutputToSpend
	for _, outputToSpend := range exchangeParams.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend() {
		output, err := entClient.TokenOutput.Query().
			Where(tokenoutput.CreatedTransactionOutputVout(int32(outputToSpend.GetPrevTokenTransactionVout())),
				tokenoutput.HasOutputCreatedTokenTransactionWith(
					tokentransaction.FinalizedTokenTransactionHashEQ(outputToSpend.GetPrevTokenTransactionHash()),
				),
			).
			WithOutputCreatedTokenTransaction().
			Only(dbCtx)
		if err != nil {
			return fmt.Errorf("failed to query token output: %w", err)
		}
		outputsToSpend = append(outputsToSpend, &tokeninternalpb.OutputToSpend{
			CreatedTokenTransactionHash: output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
			CreatedTokenTransactionVout: uint32(output.CreatedTransactionOutputVout),
			SpentTokenTransactionVout:   uint32(output.SpentTransactionInputVout),
			SpentOwnershipSignature:     output.SpentOwnershipSignature,
		})
	}

	conn, err := exchangeParams.TargetOperator.NewOperatorGRPCConnection()
	if err != nil {
		return fmt.Errorf("failed to connect to operator %s: %w", exchangeParams.TargetOperator.Identifier, err)
	}
	defer conn.Close()

	client := tokeninternalpb.NewSparkTokenInternalServiceClient(conn)

	_, err = client.ExchangeRevocationSecretsShares(ctx, &tokeninternalpb.ExchangeRevocationSecretsSharesRequest{
		FinalTokenTransaction:         exchangeParams.FinalTokenTransaction,
		FinalTokenTransactionHash:     exchangeParams.FinalTxHash,
		OperatorTransactionSignatures: allOperatorSignaturesPackage,
		OperatorShares:                exchangeParams.RevocationShares,
		OperatorIdentityPublicKey:     config.IdentityPublicKey().Serialize(),
		OutputsToSpend:                outputsToSpend,
	})
	if err != nil {
		return fmt.Errorf("failed to exchange revocation secrets with operator %s: %w", exchangeParams.TargetOperator.Identifier, err)
	}

	return nil
}

// PrepareRevocationSharesFromCoordinator prepares revocation shares from the database for testing purposes.
// This function queries the database to get the actual revocation keyshares for the outputs being spent.
func PrepareRevocationSharesFromCoordinator(
	ctx context.Context,
	config *TestWalletConfig,
	finalTokenTransaction *tokenpb.TokenTransaction,
) ([]*tokeninternalpb.OperatorRevocationShares, error) {
	client, err := ent.Open("postgres", config.CoordinatorDatabaseURI)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator database: %w", err)
	}
	defer client.Close()

	dbCtx := ent.NewContext(ctx, client)

	outputsToSpend := finalTokenTransaction.GetTransferInput().GetOutputsToSpend()
	if len(outputsToSpend) == 0 {
		return nil, fmt.Errorf("no outputs to spend found in transfer input")
	}

	var outputsWithKeyShares []*ent.TokenOutput
	for _, outputToSpend := range outputsToSpend {
		if outputToSpend == nil {
			continue
		}

		// Query the specific output by its previous transaction hash and vout
		output, err := client.TokenOutput.Query().
			Where(
				tokenoutput.HasOutputCreatedTokenTransactionWith(
					tokentransaction.FinalizedTokenTransactionHashEQ(outputToSpend.GetPrevTokenTransactionHash()),
				),
				tokenoutput.CreatedTransactionOutputVout(int32(outputToSpend.GetPrevTokenTransactionVout())),
			).
			WithRevocationKeyshare().
			WithTokenPartialRevocationSecretShares().
			Only(dbCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to query token output: %w", err)
		}

		outputsWithKeyShares = append(outputsWithKeyShares, output)
	}

	sharesToReturnMap := make(map[keys.Public]*tokeninternalpb.OperatorRevocationShares)

	allOperatorPubkeys := make([]keys.Public, 0, len(config.SigningOperators))
	for _, operator := range config.SigningOperators {
		allOperatorPubkeys = append(allOperatorPubkeys, operator.IdentityPublicKey)
	}

	for _, identityPubkey := range allOperatorPubkeys {
		sharesToReturnMap[identityPubkey] = &tokeninternalpb.OperatorRevocationShares{
			OperatorIdentityPublicKey: identityPubkey.Serialize(),
			Shares:                    make([]*tokeninternalpb.RevocationSecretShare, 0, len(outputsToSpend)),
		}
	}

	coordinator := config.SigningOperators[config.CoordinatorIdentifier]
	for _, outputWithKeyShare := range outputsWithKeyShares {
		if keyshare := outputWithKeyShare.Edges.RevocationKeyshare; keyshare != nil {
			if operatorShares, exists := sharesToReturnMap[coordinator.IdentityPublicKey]; exists {
				operatorShares.Shares = append(operatorShares.Shares, &tokeninternalpb.RevocationSecretShare{
					SecretShare: keyshare.SecretShare.Serialize(),
					InputTtxoRef: &tokenpb.TokenOutputToSpend{
						PrevTokenTransactionHash: outputWithKeyShare.CreatedTransactionFinalizedHash,
						PrevTokenTransactionVout: uint32(outputWithKeyShare.CreatedTransactionOutputVout),
					},
				})
			}
		}
		// Add any partial revocation secret shares from other operators
		if outputWithKeyShare.Edges.TokenPartialRevocationSecretShares != nil {
			for _, partialShare := range outputWithKeyShare.Edges.TokenPartialRevocationSecretShares {
				operatorKey := partialShare.OperatorIdentityPublicKey
				if operatorShares, exists := sharesToReturnMap[operatorKey]; exists {
					operatorShares.Shares = append(operatorShares.Shares, &tokeninternalpb.RevocationSecretShare{
						SecretShare: partialShare.SecretShare.Serialize(),
						InputTtxoRef: &tokenpb.TokenOutputToSpend{
							PrevTokenTransactionHash: outputWithKeyShare.CreatedTransactionFinalizedHash,
							PrevTokenTransactionVout: uint32(outputWithKeyShare.CreatedTransactionOutputVout),
						},
					})
				}
			}
		}
	}

	return slices.Collect(maps.Values(sharesToReturnMap)), nil
}

// SignHashSlice is a helper function to create either Schnorr or ECDSA signature
func SignHashSlice(config *TestWalletConfig, privKey keys.Private, hash []byte) ([]byte, error) {
	if config.UseTokenTransactionSchnorrSignatures {
		sig, err := schnorr.Sign(privKey.ToBTCEC(), hash)
		if err != nil {
			return nil, fmt.Errorf("failed to create Schnorr signature: %w", err)
		}
		return sig.Serialize(), nil
	}

	sig := ecdsa.Sign(privKey.ToBTCEC(), hash)
	return sig.Serialize(), nil
}

// QueryTokenTransactionsParams holds the parameters for QueryTokenTransactionsV2
type QueryTokenTransactionsParams struct {
	SparkAddresses    []string
	IssuerPublicKeys  []keys.Public
	OwnerPublicKeys   []keys.Public
	TokenIdentifiers  [][]byte
	OutputIDs         []string
	TransactionHashes [][]byte
	Order             pb.Order
	Offset            int64
	Limit             int64
	// Cursor-based pagination fields (used with by_filters query type)
	UseCursorPagination bool
	PageSize            uint32
	Cursor              string
	Direction           pb.Direction
}

// QueryTokenOutputs retrieves the token outputs for the given owner and token public keys.
func QueryTokenOutputs(
	ctx context.Context,
	config *TestWalletConfig,
	ownerPublicKeys []keys.Public,
	tokenPublicKeys []keys.Public,
) (*tokenpb.QueryTokenOutputsResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	tokenClient := tokenpb.NewSparkTokenServiceClient(sparkConn)

	network, err := config.Network.ToProtoNetwork()
	if err != nil {
		return nil, fmt.Errorf("failed to convert network to proto network: %w", err)
	}

	request := &tokenpb.QueryTokenOutputsRequest{
		OwnerPublicKeys:  serializeAll(ownerPublicKeys),
		IssuerPublicKeys: serializeAll(tokenPublicKeys), // Field name change: TokenPublicKeys -> IssuerPublicKeys
		Network:          network,                       // Uses pb.Network (same as sparkpb)
	}

	response, err := tokenClient.QueryTokenOutputs(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get token outputs: %w", err)
	}
	return response, nil
}

// QueryTokenTransactions retrieves token transactions for the given input filters.
func QueryTokenTransactions(
	ctx context.Context,
	config *TestWalletConfig,
	params QueryTokenTransactionsParams,
) (*tokenpb.QueryTokenTransactionsResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	tokenClient := tokenpb.NewSparkTokenServiceClient(sparkConn)

	// Decode spark addresses to get owner public keys
	var decodedOwnerPublicKeys []keys.Public
	for _, address := range params.SparkAddresses {
		decoded, err := common.DecodeSparkAddress(address)
		if err != nil {
			return nil, fmt.Errorf("failed to decode spark address: %w", err)
		}
		pubKey, err := keys.ParsePublicKey(decoded.SparkAddress.IdentityPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse identity public key from spark address: %w", err)
		}
		decodedOwnerPublicKeys = append(decodedOwnerPublicKeys, pubKey)
	}

	// Combine decoded owner public keys with direct owner public keys
	allOwnerPublicKeys := append(decodedOwnerPublicKeys, params.OwnerPublicKeys...)

	var request *tokenpb.QueryTokenTransactionsRequest

	if params.UseCursorPagination {
		// Use the by_filters query type with cursor-based pagination
		byFilters := &tokenpb.QueryTokenTransactionsByFilters{
			OutputIds:        params.OutputIDs,
			OwnerPublicKeys:  serializeAll(allOwnerPublicKeys),
			IssuerPublicKeys: serializeAll(params.IssuerPublicKeys),
			TokenIdentifiers: params.TokenIdentifiers,
			PageRequest: &pb.PageRequest{
				PageSize:  params.PageSize,
				Cursor:    params.Cursor,
				Direction: params.Direction,
			},
		}
		request = &tokenpb.QueryTokenTransactionsRequest{
			QueryType: &tokenpb.QueryTokenTransactionsRequest_ByFilters{
				ByFilters: byFilters,
			},
			Order: params.Order,
		}
	} else {
		// Use the legacy query format with offset-based pagination
		request = &tokenpb.QueryTokenTransactionsRequest{
			OwnerPublicKeys:        serializeAll(allOwnerPublicKeys),
			IssuerPublicKeys:       serializeAll(params.IssuerPublicKeys),
			TokenIdentifiers:       params.TokenIdentifiers,
			OutputIds:              params.OutputIDs,
			TokenTransactionHashes: params.TransactionHashes,
			Order:                  params.Order,
			Limit:                  params.Limit,
			Offset:                 params.Offset,
		}
	}

	response, err := tokenClient.QueryTokenTransactions(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to query token transactions: %w", err)
	}

	return response, nil
}

func serializeAll(pubKeys []keys.Public) [][]byte {
	result := make([][]byte, len(pubKeys))
	for i, key := range pubKeys {
		result[i] = key.Serialize()
	}
	return result
}

// QueryTokenMetadata retrieves token metadata for given token identifiers or issuer public keys.
func QueryTokenMetadata(
	ctx context.Context,
	config *TestWalletConfig,
	tokenIdentifiers [][]byte,
	issuerPublicKeys []keys.Public,
) (*tokenpb.QueryTokenMetadataResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	tokenClient := tokenpb.NewSparkTokenServiceClient(sparkConn)

	request := &tokenpb.QueryTokenMetadataRequest{
		TokenIdentifiers: tokenIdentifiers,
		IssuerPublicKeys: serializeAll(issuerPublicKeys),
	}

	response, err := tokenClient.QueryTokenMetadata(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to query token metadata: %w", err)
	}

	return response, nil
}
