package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pblrc20 "github.com/lightsparkdev/spark/proto/lrc20"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/lrc20"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	// Error message constants
	errIdentityPublicKeyAuthFailed        = "identity public key authentication failed"
	errInvalidPartialTokenTransaction     = "invalid partial token transaction"
	errFailedToHashPartialTransaction     = "failed to hash partial token transaction"
	errFailedToFetchPartialTransaction    = "failed to fetch partial token transaction data"
	errFailedToFetchTransaction           = "failed to fetch transaction"
	errFailedToGetUnusedKeyshares         = "failed to get unused signing keyshares"
	errNotEnoughUnusedKeyshares           = "not enough unused signing keyshares available"
	errFailedToGetNetworkFromProto        = "failed to get network from proto network"
	errFailedToExecuteWithNonCoordinator  = "failed to execute start token transaction with non-coordinator operators"
	errFailedToExecuteWithCoordinator     = "failed to execute start token transaction with coordinator"
	errFailedToGetKeyshareInfo            = "failed to get keyshare info"
	errFailedToHashFinalTransaction       = "failed to hash final token transaction"
	errFailedToConnectToOperator          = "failed to connect to operator: %s"
	errFailedToExecuteWithOperator        = "failed to execute start token transaction with operator: %s"
	errFailedToGetOperatorList            = "failed to get operator list"
	errFailedToSendToLRC20Node            = "failed to send transaction to LRC20 node"
	errFailedToUpdateOutputs              = "failed to update outputs after %s"
	errFailedToGetKeyshareForOutput       = "failed to get keyshare for output"
	errFailedToQueryTokenFreezeStatus     = "failed to query token freeze status"
	errTransactionNotCoordinatedBySO      = "transaction not coordinated by this SO"
	errFailedToGetOwnedOutputStats        = "failed to get owned output stats"
	errFailedToParseRevocationPrivateKey  = "failed to parse revocation private key"
	errFailedToValidateRevocationKeys     = "failed to validate revocation keys"
	errRevocationKeyMismatch              = "keyshare public key does not match output revocation commitment"
	errInvalidOutputs                     = "found invalid outputs"
	errInvalidInputs                      = "found invalid inputs"
	errFailedToMarshalTokenTransaction    = "failed to marshal token transaction"
	errMultipleActiveFreezes              = "multiple active freezes found for this owner and token which should not happen"
	errNoActiveFreezes                    = "no active freezes found to thaw"
	errAlreadyFrozen                      = "tokens are already frozen for this owner and token"
	errFailedToCreateTokenFreeze          = "failed to create token freeze entity"
	errFailedToUpdateTokenFreeze          = "failed to update token freeze status to thawed"
	errInvalidOutputIDFormat              = "invalid output ID format"
	errFailedToQueryTokenTransactions     = "unable to query token transactions"
	errInvalidOperatorResponse            = "invalid response from operator"
	errTransactionAlreadyFinalized        = "transaction has already been finalized by at least one operator, cannot cancel"
	errTooManyOperatorsSigned             = "transaction has been signed by %d operators, which exceeds the cancellation threshold of %d"
	errInvalidTransactionStatus           = "transaction is in status %s, but must be in %s status to cancel"
	errStoredOperatorSignatureInvalid     = "stored operator signature is invalid"
	errFailedToGetRevocationKeyshares     = "failed to get revocation keyshares for transaction"
	errFailedToConnectToOperatorForCancel = "failed to connect to operator %s"
	errFailedToQueryOperatorForCancel     = "failed to execute query with operator %s"
	errFailedToExecuteWithAllOperators    = "failed to execute query with all operators"
	errInputIndexOutOfRange               = "input index %d out of range (0-%d)"
	errInvalidOwnerSignature              = "invalid owner signature for output"
	errInvalidIssuerSignature             = "invalid issuer signature for mint"
	errFailedToHashRevocationKeyshares    = "failed to hash revocation keyshares payload"
	errTransactionHashMismatch            = "transaction hash in payload (%x) does not match actual transaction hash (%x)"
	errOperatorPublicKeyMismatch          = "operator identity public key in payload (%x) does not match this SO's identity public key (%x)"
)

// The TokenTransactionHandler is responsible for handling token transaction requests to spend and create outputs.
type TokenTransactionHandler struct {
	config      authz.Config
	db          *ent.Client
	lrc20Client *lrc20.Client
}

// NewTokenTransactionHandler creates a new TokenTransactionHandler.
func NewTokenTransactionHandler(config authz.Config, db *ent.Client, lrc20Client *lrc20.Client) *TokenTransactionHandler {
	return &TokenTransactionHandler{
		config:      config,
		db:          db,
		lrc20Client: lrc20Client,
	}
}

// StartTokenTransaction verifies the token outputs, reserves the keyshares for the token transaction, and returns metadata about the operators that possess the keyshares.
func (o TokenTransactionHandler) StartTokenTransaction(ctx context.Context, config *so.Config, req *pb.StartTokenTransactionRequest) (*pb.StartTokenTransactionResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, fmt.Errorf("%s: %w", errIdentityPublicKeyAuthFailed, err)
	}

	if err := utils.ValidatePartialTokenTransaction(req.PartialTokenTransaction, req.TokenTransactionSignatures, config.GetSigningOperatorList(), config.SupportedNetworks); err != nil {
		return nil, fmt.Errorf("%s: %w", errInvalidPartialTokenTransaction, err)
	}

	partialTokenTransactionHash, err := utils.HashTokenTransaction(req.PartialTokenTransaction, true)
	if err != nil {
		return nil, formatErrorWithTransactionProto(errFailedToHashPartialTransaction, req.PartialTokenTransaction, err)
	}

	previouslyCreatedTokenTransaction, err := ent.FetchPartialTokenTransactionData(ctx, partialTokenTransactionHash)
	if err != nil && !ent.IsNotFound(err) {
		return nil, formatErrorWithTransactionProto(errFailedToFetchPartialTransaction, req.PartialTokenTransaction, err)
	}

	// Check that the previous created transaction was found and that it is still in the started state.
	// Also, check that this SO was the coordinator for the transaction. This is necessary because only the coordinator
	// receives direct evidence from each SO individually that a threshold of SOs have validated and saved the transaction.
	if previouslyCreatedTokenTransaction != nil &&
		previouslyCreatedTokenTransaction.Status == schema.TokenTransactionStatusStarted &&
		bytes.Equal(previouslyCreatedTokenTransaction.CoordinatorPublicKey, config.IdentityPublicKey()) {
		logWithTransactionEnt(ctx, "Found existing token transaction in started state with matching coordinator",
			previouslyCreatedTokenTransaction, slog.LevelInfo)
		return o.regenerateStartResponseForDuplicateRequest(ctx, config, previouslyCreatedTokenTransaction)
	}
	// Each created output requires a keyshare for revocation key generation.
	numRevocationKeysharesNeeded := len(req.PartialTokenTransaction.TokenOutputs)
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, o.db, config, numRevocationKeysharesNeeded)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToGetUnusedKeyshares, err)
	}

	if len(keyshares) < numRevocationKeysharesNeeded {
		return nil, fmt.Errorf("%s: %d needed, %d available", errNotEnoughUnusedKeyshares, numRevocationKeysharesNeeded, len(keyshares))
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}
	network, err := common.NetworkFromProtoNetwork(req.PartialTokenTransaction.Network)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToGetNetworkFromProto, err)
	}

	// Fill revocation commitments and withdrawal bond/locktime for each output.
	finalTokenTransaction := req.PartialTokenTransaction
	for i, output := range finalTokenTransaction.TokenOutputs {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		idStr := id.String()
		output.Id = &idStr
		output.RevocationCommitment = keyshares[i].PublicKey
		withdrawalBondSats := config.Lrc20Configs[network.String()].WithdrawBondSats
		output.WithdrawBondSats = &withdrawalBondSats
		withdrawRelativeBlockLocktime := config.Lrc20Configs[network.String()].WithdrawRelativeBlockLocktime
		output.WithdrawRelativeBlockLocktime = &withdrawRelativeBlockLocktime
	}

	// Save the token transaction object to lock in the revocation commitments for each created output within this transaction.
	// Note that atomicity here is very important to ensure that the unused keyshares queried above are not used by another operation.
	// This property should be help because the coordinator blocks on the other SO responses.
	allExceptSelfSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &allExceptSelfSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		return callStartTokenTransactionInternal(ctx, operator, finalTokenTransaction, req.TokenTransactionSignatures, keyshareIDStrings, config.IdentityPublicKey())
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToExecuteWithNonCoordinator, err)
	}

	// Only save in the coordinator SO after receiving confirmation from all other SOs. This ensures that if
	// a follow up call is made that the coordiantor has only saved the data if the initial Start call reached the SO threshold.
	selfOperator := config.SigningOperatorMap[config.Identifier]
	_, err = callStartTokenTransactionInternal(ctx, selfOperator, finalTokenTransaction, req.TokenTransactionSignatures, keyshareIDStrings, config.IdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToExecuteWithCoordinator, err)
	}

	keyshareInfo, err := getStartTokenTransactionKeyshareInfo(config)
	if keyshareInfo == nil {
		return nil, fmt.Errorf("%s (hash: %x): %w",
			errFailedToGetKeyshareInfo, partialTokenTransactionHash, err)
	}

	return &pb.StartTokenTransactionResponse{
		FinalTokenTransaction: finalTokenTransaction,
		KeyshareInfo:          keyshareInfo,
	}, nil
}

// callStartTokenTransactionInternal handles calling the StartTokenTransactionInternal RPC on an operator
func callStartTokenTransactionInternal(ctx context.Context, operator *so.SigningOperator,
	finalTokenTransaction *pb.TokenTransaction, tokenTransactionSignatures *pb.TokenTransactionSignatures,
	keyshareIDStrings []string, coordinatorPublicKey []byte,
) (*emptypb.Empty, error) {
	conn, err := operator.NewGRPCConnection()
	if err != nil {
		return nil, formatErrorWithTransactionProto(fmt.Sprintf(errFailedToConnectToOperator, operator.Identifier), finalTokenTransaction, err)
	}
	defer conn.Close()

	client := pbinternal.NewSparkInternalServiceClient(conn)
	internalResp, err := client.StartTokenTransactionInternal(ctx, &pbinternal.StartTokenTransactionInternalRequest{
		KeyshareIds:                keyshareIDStrings,
		FinalTokenTransaction:      finalTokenTransaction,
		TokenTransactionSignatures: tokenTransactionSignatures,
		CoordinatorPublicKey:       coordinatorPublicKey,
	})
	if err != nil {
		return nil, formatErrorWithTransactionProto(fmt.Sprintf(errFailedToExecuteWithOperator, operator.Identifier), finalTokenTransaction, err)
	}
	return internalResp, err
}

func getStartTokenTransactionKeyshareInfo(config *so.Config) (*pb.SigningKeyshare, error) {
	allOperators := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	operatorList, err := allOperators.OperatorList(config)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToGetOperatorList, err)
	}
	operatorIdentifiers := make([]string, len(operatorList))
	for i, operator := range operatorList {
		operatorIdentifiers[i] = operator.Identifier
	}
	return &pb.SigningKeyshare{
		OwnerIdentifiers: operatorIdentifiers,
		// TODO: Unify threshold type (uint32 vs uint64) at all callsites between protos and config.
		Threshold: uint32(config.Threshold),
	}, nil
}

// validateOutputs checks if all created outputs have the expected status
func validateOutputs(outputs []*ent.TokenOutput, expectedStatus schema.TokenOutputStatus) []string {
	var invalidOutputs []string
	for i, output := range outputs {
		if output.Status != expectedStatus {
			invalidOutputs = append(invalidOutputs, fmt.Sprintf("output %d has invalid status %s, expected %s",
				i, output.Status, expectedStatus))
		}
	}
	return invalidOutputs
}

// validateInputs checks if all spent outputs have the expected status and aren't withdrawn
func validateInputs(outputs []*ent.TokenOutput, expectedStatus schema.TokenOutputStatus) []string {
	var invalidOutputs []string
	for _, output := range outputs {
		if output.Status != expectedStatus {
			invalidOutputs = append(invalidOutputs, fmt.Sprintf("input %x has invalid status %s, expected %s",
				output.ID, output.Status, expectedStatus))
		}
		if output.ConfirmedWithdrawBlockHash != nil {
			invalidOutputs = append(invalidOutputs, fmt.Sprintf("input %x is already withdrawn",
				output.ID))
		}
	}
	return invalidOutputs
}

// validateTransferOperatorSpecificSignatures validates signatures for transfer transactions
func validateTransferOperatorSpecificSignatures(identityPublicKey []byte, operatorSpecificSignatures []*pb.OperatorSpecificOwnerSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(operatorSpecificSignatures) != len(tokenTransaction.Edges.SpentOutput) {
		return formatErrorWithTransactionEnt(
			fmt.Sprintf("expected %d signatures for transfer (one per input), but got %d",
				len(tokenTransaction.Edges.SpentOutput), len(operatorSpecificSignatures)),
			tokenTransaction, nil)
	}
	numInputs := len(tokenTransaction.Edges.SpentOutput)
	signaturesByIndex := make([]*pb.OperatorSpecificOwnerSignature, numInputs)

	// Sort signatures according to index position
	for _, sig := range operatorSpecificSignatures {
		index := int(sig.OwnerSignature.InputIndex)
		if index < 0 || index >= numInputs {
			return formatErrorWithTransactionEnt(
				fmt.Sprintf(errInputIndexOutOfRange, index, numInputs-1),
				tokenTransaction, nil)
		}

		if signaturesByIndex[index] != nil {
			return formatErrorWithTransactionEnt(
				fmt.Sprintf("duplicate signature for input index %d", index),
				tokenTransaction, nil)
		}

		signaturesByIndex[index] = sig
	}

	for i := 0; i < numInputs; i++ {
		if signaturesByIndex[i] == nil {
			return formatErrorWithTransactionEnt(
				fmt.Sprintf("missing signature for input index %d", i),
				tokenTransaction, nil)
		}
	}

	// Sort spent outputs by their index
	spentOutputs := make([]*ent.TokenOutput, numInputs)
	copy(spentOutputs, tokenTransaction.Edges.SpentOutput)
	sort.Slice(spentOutputs, func(i, j int) bool {
		return spentOutputs[i].SpentTransactionInputVout < spentOutputs[j].SpentTransactionInputVout
	})

	// Validate each signature against its corresponding output
	for i, sig := range signaturesByIndex {
		payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(sig.Payload)
		if err != nil {
			return fmt.Errorf("%s: %w", errFailedToHashRevocationKeyshares, err)
		}

		if !bytes.Equal(sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash) {
			return fmt.Errorf(errTransactionHashMismatch,
				sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash)
		}

		if !bytes.Equal(sig.Payload.OperatorIdentityPublicKey, identityPublicKey) {
			return fmt.Errorf(errOperatorPublicKeyMismatch,
				sig.Payload.OperatorIdentityPublicKey, identityPublicKey)
		}

		output := spentOutputs[i]
		if err := utils.ValidateOwnershipSignature(
			sig.OwnerSignature.Signature,
			payloadHash,
			output.OwnerPublicKey,
		); err != nil {
			return formatErrorWithTransactionEnt(errInvalidOwnerSignature, tokenTransaction, err)
		}
	}

	return nil
}

// validateMintOperatorSpecificSignatures validates signatures for mint transactions
func validateMintOperatorSpecificSignatures(identityPublicKey []byte, operatorSpecificSignatures []*pb.OperatorSpecificOwnerSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(operatorSpecificSignatures) != 1 {
		return formatErrorWithTransactionEnt(
			fmt.Sprintf("expected exactly 1 signature for mint, but got %d",
				len(operatorSpecificSignatures)),
			tokenTransaction, nil)
	}

	if tokenTransaction.Edges.Mint == nil {
		return formatErrorWithTransactionEnt(
			"mint record not found in db, but expected a mint for this transaction",
			tokenTransaction, nil)
	}

	sig := operatorSpecificSignatures[0]

	// Validate the signature payload
	payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(sig.Payload)
	if err != nil {
		return fmt.Errorf("%s: %w", errFailedToHashRevocationKeyshares, err)
	}

	if !bytes.Equal(sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash) {
		return fmt.Errorf(errTransactionHashMismatch,
			sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash)
	}

	if len(sig.Payload.OperatorIdentityPublicKey) > 0 {
		if !bytes.Equal(sig.Payload.OperatorIdentityPublicKey, identityPublicKey) {
			return fmt.Errorf(errOperatorPublicKeyMismatch,
				sig.Payload.OperatorIdentityPublicKey, identityPublicKey)
		}
	}

	// Validate the signature using the issuer public key from the database
	if err := utils.ValidateOwnershipSignature(
		sig.OwnerSignature.Signature,
		payloadHash,
		tokenTransaction.Edges.Mint.IssuerPublicKey,
	); err != nil {
		return formatErrorWithTransactionEnt(errInvalidIssuerSignature, tokenTransaction, err)
	}

	return nil
}

func validateTokenTransactionForSigning(tokenTransactionProto *pb.TokenTransaction, tokenTransactionEnt *ent.TokenTransaction) error {
	if tokenTransactionEnt.Status != schema.TokenTransactionStatusStarted &&
		tokenTransactionEnt.Status != schema.TokenTransactionStatusSigned {
		return fmt.Errorf("signing failed because transaction is not in correct state, expected %s or %s, current status: %s", schema.TokenTransactionStatusStarted, schema.TokenTransactionStatusSigned, tokenTransactionEnt.Status)
	}
	if len(tokenTransactionProto.GetTransferInput().GetOutputsToSpend()) != len(tokenTransactionEnt.Edges.SpentOutput) {
		return fmt.Errorf("signing failed because transaction is no longer mapped to spent TTXOs. Expected %d TTXOs, found %d. TTXOs were likely remapped to a more recent started transaction", len(tokenTransactionProto.GetTransferInput().GetOutputsToSpend()), len(tokenTransactionEnt.Edges.SpentOutput))
	}
	if !tokenTransactionEnt.ExpiryTime.IsZero() && time.Now().After(tokenTransactionEnt.ExpiryTime) {
		return fmt.Errorf("signing failed because token transaction %s has expired at %s", tokenTransactionEnt.ID, tokenTransactionEnt.ExpiryTime.Format(time.RFC3339))
	}
	return nil
}

// validateOperatorSpecificSignatures validates the signatures in the request against the transaction hash
// and verifies that the number of signatures matches the expected count based on transaction type
func validateOperatorSpecificSignatures(identityPublicKey []byte, operatorSpecificSignatures []*pb.OperatorSpecificOwnerSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		return validateTransferOperatorSpecificSignatures(identityPublicKey, operatorSpecificSignatures, tokenTransaction)
	}
	return validateMintOperatorSpecificSignatures(identityPublicKey, operatorSpecificSignatures, tokenTransaction)
}

// SignTokenTransaction signs the token transaction with the operators private key.
// If it is a transfer it also fetches this operators keyshare for each spent output and
// returns it to the wallet so it can finalize the transaction.
func (o TokenTransactionHandler) SignTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.SignTokenTransactionRequest,
) (*pb.SignTokenTransactionResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	finalTokenTransactionHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToHashFinalTransaction, err)
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToFetchTransaction, err)
	}
	if err := validateTokenTransactionForSigning(req.FinalTokenTransaction, tokenTransaction); err != nil {
		return nil, formatErrorWithTransactionEnt(err.Error(), tokenTransaction, err)
	}

	if err := validateOperatorSpecificSignatures(config.IdentityPublicKey(), req.OperatorSpecificSignatures, tokenTransaction); err != nil {
		return nil, err
	}

	if tokenTransaction.Status == schema.TokenTransactionStatusSigned {
		return o.regenerateSigningResponseForDuplicateRequest(ctx, config, tokenTransaction, finalTokenTransactionHash)
	}

	invalidOutputs := validateOutputs(tokenTransaction.Edges.CreatedOutput, schema.TokenOutputStatusCreatedStarted)
	if len(invalidOutputs) > 0 {
		return nil, formatErrorWithTransactionEnt(fmt.Sprintf("%s: %s", errInvalidOutputs, strings.Join(invalidOutputs, "; ")), tokenTransaction, nil)
	}

	// If token outputs are being spent, verify the expected status of inputs and check for active freezes.
	// For mints this is not necessary and will be skipped because it does not spend outputs.
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs := validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentStarted)
		if len(invalidOutputs) > 0 {
			return nil, formatErrorWithTransactionEnt(fmt.Sprintf("%s: %s", errInvalidInputs, strings.Join(invalidOutputs, "; ")), tokenTransaction, nil)
		}

		// Collect owner public keys for freeze check.
		ownerPublicKeys := make([][]byte, len(tokenTransaction.Edges.SpentOutput))
		// Assumes that all token public keys are the same as the first output. This is asserted when validating
		// in the StartTokenTransaction() step.
		tokenPublicKey := tokenTransaction.Edges.SpentOutput[0].TokenPublicKey
		for i, output := range tokenTransaction.Edges.SpentOutput {
			ownerPublicKeys[i] = output.OwnerPublicKey
		}

		// Bulk query all input ids to ensure none of them are frozen.
		activeFreezes, err := ent.GetActiveFreezes(ctx, ownerPublicKeys, tokenPublicKey)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errFailedToQueryTokenFreezeStatus, err)
		}

		if len(activeFreezes) > 0 {
			for _, freeze := range activeFreezes {
				logger.Info("Found active freeze", "owner", freeze.OwnerPublicKey, "token", freeze.TokenPublicKey, "freeze_timestamp", freeze.WalletProvidedFreezeTimestamp)
			}
			return nil, fmt.Errorf("at least one input is frozen. Cannot proceed with transaction")
		}
	}

	identityPrivateKey := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey)
	operatorSignature := ecdsa.Sign(identityPrivateKey, finalTokenTransactionHash)

	// Order the signatures according to their index before updating the DB.
	operatorSpecificSignatureMap := make(map[int][]byte, len(req.OperatorSpecificSignatures))
	for _, sig := range req.OperatorSpecificSignatures {
		inputIndex := int(sig.OwnerSignature.InputIndex)
		operatorSpecificSignatureMap[inputIndex] = sig.OwnerSignature.Signature
	}
	operatorSpecificSignatures := make([][]byte, len(operatorSpecificSignatureMap))
	for i := 0; i < len(operatorSpecificSignatureMap); i++ {
		operatorSpecificSignatures[i] = operatorSpecificSignatureMap[i]
	}
	err = ent.UpdateSignedTransaction(ctx, tokenTransaction, operatorSpecificSignatures, operatorSignature.Serialize())
	if err != nil {
		return nil, formatErrorWithTransactionEnt(fmt.Sprintf(errFailedToUpdateOutputs, "signing"), tokenTransaction, err)
	}

	operatorSignatureData := &pblrc20.SparkOperatorSignatureData{
		SparkOperatorSignature:    operatorSignature.Serialize(),
		OperatorIdentityPublicKey: identityPrivateKey.PubKey().SerializeCompressed(),
	}

	keyshares := make([]*ent.SigningKeyshare, len(tokenTransaction.Edges.SpentOutput))
	revocationKeyshares := make([]*pb.KeyshareWithIndex, len(tokenTransaction.Edges.SpentOutput))
	for _, output := range tokenTransaction.Edges.SpentOutput {
		keyshare, err := output.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			logger.Info("Failed to get keyshare for output", "error", err)
			return nil, err
		}
		index := output.SpentTransactionInputVout
		keyshares[index] = keyshare
		revocationKeyshares[index] = &pb.KeyshareWithIndex{
			InputIndex: uint32(index),
			Keyshare:   keyshare.SecretShare,
		}

		// Validate that the keyshare's public key is as expected.
		if !bytes.Equal(keyshare.PublicKey, output.WithdrawRevocationCommitment) {
			return nil, fmt.Errorf(
				"keyshare public key %x does not match output revocation commitment %x",
				keyshare.PublicKey,
				output.WithdrawRevocationCommitment,
			)
		}
	}

	sparkSigReq := &pblrc20.SendSparkSignatureRequest{
		FinalTokenTransaction:      req.FinalTokenTransaction,
		OperatorSpecificSignatures: req.OperatorSpecificSignatures,
		OperatorSignatureData:      operatorSignatureData,
	}

	err = o.lrc20Client.SendSparkSignature(ctx, sparkSigReq)
	if err != nil {
		logger.Error("Failed to send transaction to LRC20 node", "error", err)
		return nil, err
	}

	return &pb.SignTokenTransactionResponse{
		SparkOperatorSignature: operatorSignature.Serialize(),
		RevocationKeyshares:    revocationKeyshares,
	}, nil
}

// regenerateStartResponseForDuplicateRequest handles the case where a Start() recall has been received for a
// partial token transaction which has already been started. This allows for simpler wallet SDK logic such that
// if a later SignTokenTransaction() call to one of the SOs failed- the wallet SDK can retry from the beginning
// and retrieve the original final token transaction which was started before signing among all parties.
// This does not allow for retrying a Start call that was incomplete due to a downstream error.  A repeat
// request for the same transaction that was not fully started will generate a fresh final token transaction
// with different revocation keys.
func (o TokenTransactionHandler) regenerateStartResponseForDuplicateRequest(
	ctx context.Context,
	config *so.Config,
	tokenTransaction *ent.TokenTransaction,
) (*pb.StartTokenTransactionResponse, error) {
	logWithTransactionEnt(ctx, "Regenerating response for a duplicate StartTokenTransaction() Call", tokenTransaction, slog.LevelDebug)

	var invalidOutputs []string
	expectedCreatedOutputStatus := schema.TokenOutputStatusCreatedStarted

	invalidOutputs = validateOutputs(tokenTransaction.Edges.CreatedOutput, expectedCreatedOutputStatus)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentStarted)...)
	}
	if len(invalidOutputs) > 0 {
		return nil, formatErrorWithTransactionEnt(
			fmt.Sprintf("%s: %s",
				errInvalidOutputs,
				strings.Join(invalidOutputs, "; ")),
			tokenTransaction, nil)
	}

	// Reconstruct the token transaction from the ent data.
	transaction, err := tokenTransaction.MarshalProto(config)
	if err != nil {
		return nil, formatErrorWithTransactionEnt(errFailedToMarshalTokenTransaction, tokenTransaction, err)
	}

	keyshareInfo, err := getStartTokenTransactionKeyshareInfo(config)
	if keyshareInfo == nil {
		return nil, formatErrorWithTransactionEnt(errFailedToGetKeyshareInfo, tokenTransaction, err)
	}

	logWithTransactionEnt(ctx, "Returning stored final token transaction in response to repeat start call",
		tokenTransaction, slog.LevelDebug)
	return &pb.StartTokenTransactionResponse{
		FinalTokenTransaction: transaction,
		KeyshareInfo:          keyshareInfo,
	}, nil
}

// regenerateSigningResponseForDuplicateRequest handles the case where a transaction has already been signed.
// This allows for simpler wallet SDK logic such that if a Sign() call to one of the SOs failed,
// the wallet SDK can retry with all SOs and get successful responses.
func (o TokenTransactionHandler) regenerateSigningResponseForDuplicateRequest(
	ctx context.Context,
	config *so.Config,
	tokenTransaction *ent.TokenTransaction,
	finalTokenTransactionHash []byte,
) (*pb.SignTokenTransactionResponse, error) {
	logWithTransactionEnt(ctx, "Regenerating response for a duplicate SignTokenTransaction() Call", tokenTransaction, slog.LevelDebug)

	var invalidOutputs []string
	isMint := tokenTransaction.Edges.Mint != nil
	expectedCreatedOutputStatus := schema.TokenOutputStatusCreatedSigned
	if isMint {
		expectedCreatedOutputStatus = schema.TokenOutputStatusCreatedFinalized
	}

	invalidOutputs = validateOutputs(tokenTransaction.Edges.CreatedOutput, expectedCreatedOutputStatus)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentSigned)...)
	}
	if len(invalidOutputs) > 0 {
		return nil, formatErrorWithTransactionEnt(
			fmt.Sprintf("%s: %s",
				errInvalidOutputs,
				strings.Join(invalidOutputs, "; ")),
			tokenTransaction, nil)
	}

	if err := utils.ValidateOwnershipSignature(
		tokenTransaction.OperatorSignature,
		finalTokenTransactionHash,
		config.IdentityPublicKey(),
	); err != nil {
		return nil, formatErrorWithTransactionEnt(errStoredOperatorSignatureInvalid, tokenTransaction, err)
	}

	revocationKeyshares, err := o.getRevocationKeysharesForTokenTransaction(ctx, tokenTransaction)
	if err != nil {
		return nil, formatErrorWithTransactionEnt(errFailedToGetRevocationKeyshares, tokenTransaction, err)
	}
	logWithTransactionEnt(ctx, "Returning stored signature in response to repeat Sign() call", tokenTransaction, slog.LevelDebug)
	return &pb.SignTokenTransactionResponse{
		SparkOperatorSignature: tokenTransaction.OperatorSignature,
		RevocationKeyshares:    revocationKeyshares,
	}, nil
}

// FinalizeTokenTransaction takes the revocation private keys for spent outputs and updates their status to finalized.
func (o TokenTransactionHandler) FinalizeTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.FinalizeTokenTransactionRequest,
) (*emptypb.Empty, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, fmt.Errorf("%s: %w", errIdentityPublicKeyAuthFailed, err)
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, formatErrorWithTransactionEnt(errFailedToFetchTransaction, tokenTransaction, err)
	}

	// Verify that the transaction is in a signed state before finalizing
	if tokenTransaction.Status != schema.TokenTransactionStatusSigned {
		return nil, formatErrorWithTransactionEnt(
			fmt.Sprintf(errInvalidTransactionStatus,
				tokenTransaction.Status, schema.TokenTransactionStatusSigned),
			tokenTransaction, nil)
	}

	// Verify status of created outputs and spent outputs
	invalidOutputs := validateOutputs(tokenTransaction.Edges.CreatedOutput, schema.TokenOutputStatusCreatedSigned)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentSigned)...)
	}

	if len(invalidOutputs) > 0 {
		return nil, formatErrorWithTransactionEnt(fmt.Sprintf("%s: %s", errInvalidOutputs, strings.Join(invalidOutputs, "; ")), tokenTransaction, nil)
	}

	if len(tokenTransaction.Edges.SpentOutput) != len(req.RevocationSecrets) {
		return nil, formatErrorWithTransactionEnt(
			fmt.Sprintf("number of revocation keys (%d) does not match number of spent outputs (%d)",
				len(req.RevocationSecrets),
				len(tokenTransaction.Edges.SpentOutput)),
			tokenTransaction, nil)
	}
	revocationSecretMap := make(map[int][]byte)
	for _, revocationSecret := range req.RevocationSecrets {
		revocationSecretMap[int(revocationSecret.InputIndex)] = revocationSecret.RevocationSecret
	}
	// Validate that we have exactly one revocation secret for each input index
	// and that they form a contiguous sequence from 0 to len(tokenTransaction.Edges.SpentOutput)-1
	for i := 0; i < len(tokenTransaction.Edges.SpentOutput); i++ {
		if _, exists := revocationSecretMap[i]; !exists {
			return nil, formatErrorWithTransactionEnt(
				fmt.Sprintf("missing revocation secret for input index %d", i),
				tokenTransaction, nil)
		}
	}

	revocationSecrets := make([]*secp256k1.PrivateKey, len(revocationSecretMap))
	revocationCommitements := make([][]byte, len(revocationSecretMap))

	spentOutputs := make([]*ent.TokenOutput, len(tokenTransaction.Edges.SpentOutput))
	copy(spentOutputs, tokenTransaction.Edges.SpentOutput)
	sort.Slice(spentOutputs, func(i, j int) bool {
		return spentOutputs[i].SpentTransactionInputVout < spentOutputs[j].SpentTransactionInputVout
	})

	// Match each output with its corresponding revocation secret
	for i, output := range spentOutputs {
		index := int(output.SpentTransactionInputVout)
		revocationSecret, exists := revocationSecretMap[index]
		if !exists {
			return nil, formatErrorWithTransactionEnt(
				fmt.Sprintf("missing revocation secret for input at index %d", index),
				tokenTransaction, nil)
		}

		revocationPrivateKey, err := common.PrivateKeyFromBytes(revocationSecret)
		if err != nil {
			return nil, formatErrorWithTransactionEnt(errFailedToParseRevocationPrivateKey, tokenTransaction, err)
		}

		revocationSecrets[i] = revocationPrivateKey
		revocationCommitements[i] = output.WithdrawRevocationCommitment
	}

	err = utils.ValidateRevocationKeys(revocationSecrets, revocationCommitements)
	if err != nil {
		return nil, formatErrorWithTransactionEnt(errFailedToValidateRevocationKeys, tokenTransaction, err)
	}

	identityPrivateKey := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey)

	err = o.lrc20Client.SendSparkSignature(ctx, o.buildLrc20SendSignaturesRequest(
		req.FinalTokenTransaction,
		tokenTransaction.OperatorSignature,
		identityPrivateKey,
		req.RevocationSecrets,
	))
	if err != nil {
		return nil, formatErrorWithTransactionEnt(errFailedToSendToLRC20Node, tokenTransaction, err)
	}

	err = ent.UpdateFinalizedTransaction(ctx, tokenTransaction, req.RevocationSecrets)
	if err != nil {
		return nil, formatErrorWithTransactionEnt(fmt.Sprintf(errFailedToUpdateOutputs, "finalizing"), tokenTransaction, err)
	}

	return &emptypb.Empty{}, nil
}

// FreezeTokens freezes or unfreezes tokens on the LRC20 node.
func (o TokenTransactionHandler) FreezeTokens(
	ctx context.Context,
	req *pb.FreezeTokensRequest,
) (*pb.FreezeTokensResponse, error) {
	freezePayloadHash, err := utils.HashFreezeTokensPayload(req.FreezeTokensPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to hash freeze tokens payload: %w", err)
	}

	if err := utils.ValidateOwnershipSignature(
		req.IssuerSignature,
		freezePayloadHash,
		req.FreezeTokensPayload.TokenPublicKey,
	); err != nil {
		return nil, fmt.Errorf("invalid issuer signature to freeze token public key %x: %w", req.FreezeTokensPayload.TokenPublicKey, err)
	}

	// Check for existing freeze.
	activeFreezes, err := ent.GetActiveFreezes(ctx, [][]byte{req.FreezeTokensPayload.OwnerPublicKey}, req.FreezeTokensPayload.TokenPublicKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToQueryTokenFreezeStatus, err)
	}
	if req.FreezeTokensPayload.ShouldUnfreeze {
		if len(activeFreezes) == 0 {
			return nil, fmt.Errorf("no active freezes found to thaw")
		}
		if len(activeFreezes) > 1 {
			return nil, fmt.Errorf("%s", errMultipleActiveFreezes)
		}
		err = ent.ThawActiveFreeze(ctx, activeFreezes[0].ID, req.FreezeTokensPayload.IssuerProvidedTimestamp)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errFailedToUpdateTokenFreeze, err)
		}
	} else { // Freeze
		if len(activeFreezes) > 0 {
			return nil, fmt.Errorf("%s", errAlreadyFrozen)
		}
		err = ent.ActivateFreeze(ctx,
			req.FreezeTokensPayload.OwnerPublicKey,
			req.FreezeTokensPayload.TokenPublicKey,
			req.IssuerSignature,
			req.FreezeTokensPayload.IssuerProvidedTimestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errFailedToCreateTokenFreeze, err)
		}
	}

	// Collect information about the frozen outputs.
	outputIDs, totalAmount, err := ent.GetOwnedTokenOutputStats(ctx, [][]byte{req.FreezeTokensPayload.OwnerPublicKey}, req.FreezeTokensPayload.TokenPublicKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToGetOwnedOutputStats, err)
	}

	err = o.FreezeTokensOnLRC20Node(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errFailedToSendToLRC20Node, err)
	}

	return &pb.FreezeTokensResponse{
		ImpactedOutputIds:   outputIDs,
		ImpactedTokenAmount: totalAmount.Bytes(),
	}, nil
}

// FreezeTokensOnLRC20Node freezes or unfreezes tokens on the LRC20 node.
func (o TokenTransactionHandler) FreezeTokensOnLRC20Node(
	ctx context.Context,
	req *pb.FreezeTokensRequest,
) error {
	return o.lrc20Client.FreezeTokens(ctx, req)
}

// QueryTokenTransactions returns SO provided data about specific token transactions along with their status.
// Allows caller to specify data to be returned related to:
// a) transactions associated with a particular set of output ids
// b) transactions associated with a particular set of transaction hashes
// c) all transactions associated with a particular token public key
func (o TokenTransactionHandler) QueryTokenTransactions(ctx context.Context, config *so.Config, req *pb.QueryTokenTransactionsRequest) (*pb.QueryTokenTransactionsResponse, error) {
	db := ent.GetDbFromContext(ctx)

	// Start with a base query for token transactions
	baseQuery := db.TokenTransaction.Query()

	// Apply filters based on request parameters
	if len(req.OutputIds) > 0 {
		// Convert string IDs to UUIDs
		outputUUIDs := make([]uuid.UUID, 0, len(req.OutputIds))
		for _, idStr := range req.OutputIds {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return nil, fmt.Errorf("invalid output ID format: %v", err)
			}
			outputUUIDs = append(outputUUIDs, id)
		}

		// Find transactions that created or spent these outputs
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.IDIn(outputUUIDs...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.IDIn(outputUUIDs...)),
			),
		)
	}

	if len(req.TokenTransactionHashes) > 0 {
		baseQuery = baseQuery.Where(tokentransaction.FinalizedTokenTransactionHashIn(req.TokenTransactionHashes...))
	}

	if len(req.TokenPublicKeys) > 0 {
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.TokenPublicKeyIn(req.TokenPublicKeys...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.TokenPublicKeyIn(req.TokenPublicKeys...)),
			),
		)
	}

	// Apply sorting, limit and offset
	query := baseQuery.Order(ent.Desc(tokentransaction.FieldUpdateTime))

	if req.Limit > 100 || req.Limit == 0 {
		req.Limit = 100
	}
	query = query.Limit(int(req.Limit))

	if req.Offset > 0 {
		query = query.Offset(int(req.Offset))
	}

	// This join respects the query limitations provided above and should only load the necessary relations.
	query = query.
		WithCreatedOutput().
		WithSpentOutput(func(slq *ent.TokenOutputQuery) {
			slq.WithOutputCreatedTokenTransaction()
		}).WithMint()

	// Execute the query
	transactions, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query token transactions: %v", err)
	}

	// Convert to response protos
	transactionsWithStatus := make([]*pb.TokenTransactionWithStatus, 0, len(transactions))
	for _, transaction := range transactions {
		// Determine transaction status based on output statuses.
		status := convertTokenTransactionStatus(transaction.Status)

		// Reconstruct the token transaction from the ent data.
		transactionProto, err := transaction.MarshalProto(config)
		if err != nil {
			return nil, formatErrorWithTransactionEnt(errFailedToMarshalTokenTransaction, transaction, err)
		}

		// This would require reconstructing the transaction from the database
		// For now, we'll just include the transaction hash.
		transactionsWithStatus = append(transactionsWithStatus, &pb.TokenTransactionWithStatus{
			TokenTransaction: transactionProto,
			Status:           status,
		})
	}

	// Calculate next offset
	var nextOffset int64
	if len(transactions) == int(req.Limit) {
		nextOffset = req.Offset + int64(len(transactions))
	} else {
		nextOffset = -1
	}

	return &pb.QueryTokenTransactionsResponse{
		TokenTransactionsWithStatus: transactionsWithStatus,
		Offset:                      nextOffset,
	}, nil
}

func (o TokenTransactionHandler) QueryTokenOutputs(
	ctx context.Context,
	config *so.Config,
	req *pb.QueryTokenOutputsRequest,
) (*pb.QueryTokenOutputsResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	responses, err := helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection,
		func(ctx context.Context, operator *so.SigningOperator) (map[string]*pb.OutputWithPreviousTransactionData, error) {
			conn, err := operator.NewGRPCConnection()
			if err != nil {
				return nil, fmt.Errorf("failed to connect to operator %s: %w", operator.Identifier, err)
			}
			defer conn.Close()

			client := pbinternal.NewSparkInternalServiceClient(conn)
			availableOutputs, err := client.QueryTokenOutputsInternal(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("failed to query token outputs from operator %s: %w", operator.Identifier, err)
			}
			spendableOutputMap := make(map[string]*pb.OutputWithPreviousTransactionData)
			for _, output := range availableOutputs.OutputsWithPreviousTransactionData {
				spendableOutputMap[*output.Output.Id] = output
			}
			return spendableOutputMap, nil
		},
	)
	if err != nil {
		logger.Info("failed to query token outputs from operators", "error", err)
		return nil, fmt.Errorf("failed to query token outputs from operators: %w", err)
	}

	// Only return token outputs to the wallet that ALL SOs agree are spendable.
	//
	// If a TTXO is partially signed, the spending transaction will be cancelled once it expires to return the TTXO to the wallet.
	spendableOutputs := make([]*pb.OutputWithPreviousTransactionData, 0)
	countSpendableOperatorsForOutputID := make(map[string]int)

	requiredSpendableOperators := len(config.GetSigningOperatorList())
	for _, spendableOutputMap := range responses {
		for outputID, spendableOutput := range spendableOutputMap {
			countSpendableOperatorsForOutputID[outputID]++
			if countSpendableOperatorsForOutputID[outputID] == requiredSpendableOperators {
				spendableOutputs = append(spendableOutputs, spendableOutput)
			}
		}
	}

	for outputID, countSpendableOperators := range countSpendableOperatorsForOutputID {
		if countSpendableOperators < requiredSpendableOperators {
			logger.Warn("token output not spendable in all operators",
				"outputID", outputID,
				"countSpendableOperators", countSpendableOperators,
			)
		}
	}

	return &pb.QueryTokenOutputsResponse{
		OutputsWithPreviousTransactionData: spendableOutputs,
	}, nil
}

func (o TokenTransactionHandler) CancelSignedTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.CancelSignedTokenTransactionRequest,
) (*emptypb.Empty, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.SenderIdentityPublicKey); err != nil {
		return nil, err
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, formatErrorWithTransactionProto(errFailedToFetchTransaction, req.FinalTokenTransaction, err)
	}

	// Verify that the transaction is in a signed state locally
	if tokenTransaction.Status != schema.TokenTransactionStatusSigned {
		return nil, formatErrorWithTransactionEnt(
			fmt.Sprintf(errInvalidTransactionStatus,
				tokenTransaction.Status, schema.TokenTransactionStatusSigned),
			tokenTransaction, nil)
	}

	// Verify with the other SOs that the transaction is in a cancellable state.
	// Each SO verifies that:
	// 1. No SO has moved the transaction to a 'Finalized' state.
	// 2. (# of SOs) - threshold have not progressed the transaction to a 'Signed' state.
	// TODO: In the future it may be possible to optimize these constraints in two ways:
	// a) Don't check for (1) because if a user finalizes before threshold has signed and then tries to cancel afterwords they effectively sacrifice their funds.
	// b) Update (2) to not ping every SO in parallel but ping one at a time until # SOs - threshold have validated that they have not yet signed.
	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	responses, err := helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, formatErrorWithTransactionEnt(
				fmt.Sprintf(errFailedToConnectToOperatorForCancel, operator.Identifier),
				tokenTransaction, err)
		}
		defer conn.Close()

		client := pb.NewSparkServiceClient(conn)
		internalResp, err := client.QueryTokenTransactions(ctx, &pb.QueryTokenTransactionsRequest{
			TokenTransactionHashes: [][]byte{tokenTransaction.FinalizedTokenTransactionHash},
		})
		if err != nil {
			return nil, formatErrorWithTransactionEnt(
				fmt.Sprintf(errFailedToQueryOperatorForCancel, operator.Identifier),
				tokenTransaction, err)
		}
		return internalResp, err
	})
	if err != nil {
		return nil, formatErrorWithTransactionEnt(errFailedToExecuteWithAllOperators, tokenTransaction, err)
	}

	// Check if any operator has finalized the transaction
	signedCount := 0
	for _, resp := range responses {
		queryResp, ok := resp.(*pb.QueryTokenTransactionsResponse)
		if !ok || queryResp == nil {
			return nil, formatErrorWithTransactionEnt("invalid response from operator", tokenTransaction, nil)
		}

		for _, txWithStatus := range queryResp.TokenTransactionsWithStatus {
			if txWithStatus.Status == pb.TokenTransactionStatus_TOKEN_TRANSACTION_FINALIZED {
				return nil, formatErrorWithTransactionEnt("transaction has already been finalized by at least one operator, cannot cancel", tokenTransaction, nil)
			}
			if txWithStatus.Status == pb.TokenTransactionStatus_TOKEN_TRANSACTION_SIGNED ||
				// Check for this just in case. Its unlikely, but it is theoretically possible for a race condition where
				// the transaction is signed by the final operator needed for threshold just as the transaction is cancelled by a
				// different operator. In this event, the operators that didn't cancel yet should not cancel to avoid a fully
				// signed transaction being cancelled in all SOs.
				// TODO(DL-140): Better handle this race condition, likely by allowing SIGNED_CANCELLED to transition into FINALIZED
				// if a revocation secret is provided (which proves that all SOs have signed)
				txWithStatus.Status == pb.TokenTransactionStatus_TOKEN_TRANSACTION_SIGNED_CANCELLED {
				signedCount++
			}
		}
	}

	// Check if too many operators have already signed
	operatorCount := len(config.GetSigningOperatorList())
	threshold := int(config.Threshold)
	if signedCount > operatorCount-threshold {
		return nil, formatErrorWithTransactionEnt(
			fmt.Sprintf("transaction has been signed by %d operators, which exceeds the cancellation threshold of %d",
				signedCount, operatorCount-threshold),
			tokenTransaction, nil)
	}

	err = ent.UpdateCancelledTransaction(ctx, tokenTransaction)
	if err != nil {
		return nil, formatErrorWithTransactionEnt(fmt.Sprintf(errFailedToUpdateOutputs, "canceling"), tokenTransaction, err)
	}

	return &emptypb.Empty{}, nil
}

// getRevocationKeysharesForTokenTransaction retrieves the revocation keyshares for a token transaction
func (o TokenTransactionHandler) getRevocationKeysharesForTokenTransaction(ctx context.Context, tokenTransaction *ent.TokenTransaction) ([]*pb.KeyshareWithIndex, error) {
	spentOutputs := tokenTransaction.Edges.SpentOutput
	revocationKeyshares := make([]*pb.KeyshareWithIndex, len(spentOutputs))
	for i, output := range spentOutputs {
		keyshare, err := output.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			return nil, formatErrorWithTransactionEnt(errFailedToGetKeyshareForOutput, tokenTransaction, err)
		}
		// Validate that the keyshare's public key is as expected.
		if !bytes.Equal(keyshare.PublicKey, output.WithdrawRevocationCommitment) {
			return nil, formatErrorWithTransactionEnt(
				fmt.Sprintf("%s: %x does not match %x",
					errRevocationKeyMismatch, keyshare.PublicKey, output.WithdrawRevocationCommitment),
				tokenTransaction, nil)
		}

		revocationKeyshares[i] = &pb.KeyshareWithIndex{
			InputIndex: uint32(output.SpentTransactionInputVout),
			Keyshare:   keyshare.SecretShare,
		}
	}
	// Sort spent output keyshares by their index to ensure a consistent response
	sort.Slice(revocationKeyshares, func(i, j int) bool {
		return revocationKeyshares[i].InputIndex < revocationKeyshares[j].InputIndex
	})

	return revocationKeyshares, nil
}

func (o TokenTransactionHandler) buildLrc20SendSignaturesRequest(finalTokenTransaction *pb.TokenTransaction, operatorSignature []byte, identityPrivateKey *secp256k1.PrivateKey, revocationSecrets []*pb.RevocationSecretWithIndex) *pblrc20.SendSparkSignatureRequest {
	operatorSignatureData := &pblrc20.SparkOperatorSignatureData{
		SparkOperatorSignature:    operatorSignature,
		OperatorIdentityPublicKey: identityPrivateKey.PubKey().SerializeCompressed(),
	}

	return &pblrc20.SendSparkSignatureRequest{
		FinalTokenTransaction: finalTokenTransaction,
		OperatorSignatureData: operatorSignatureData,
		RevocationSecrets:     revocationSecrets,
	}
}

func logWithTransactionEnt(ctx context.Context, msg string, tokenTransaction *ent.TokenTransaction, level slog.Level) {
	logger := logging.GetLoggerFromContext(ctx)

	attrs := []any{
		"transaction_uuid", tokenTransaction.ID.String(),
		"transaction_hash", hex.EncodeToString(tokenTransaction.FinalizedTokenTransactionHash),
	}

	logger.Log(ctx, level, msg, attrs...)
}

func formatErrorWithTransactionEnt(msg string, tokenTransaction *ent.TokenTransaction, err error) error {
	return fmt.Errorf("%s (uuid: %s, hash: %x): %w",
		msg,
		tokenTransaction.ID.String(),
		tokenTransaction.FinalizedTokenTransactionHash,
		err)
}

func formatErrorWithTransactionProto(msg string, tokenTransaction *pb.TokenTransaction, err error) error {
	if err != nil {
		return fmt.Errorf("%s (transaction: %s): %w",
			msg,
			tokenTransaction.String(),
			err)
	}
	return fmt.Errorf("%s (transaction: %s)",
		msg,
		tokenTransaction.String())
}

// convertTokenTransactionStatus converts from schema.TokenTransactionStatus to pb.TokenTransactionStatus
func convertTokenTransactionStatus(status schema.TokenTransactionStatus) pb.TokenTransactionStatus {
	switch status {
	case schema.TokenTransactionStatusStarted:
		return pb.TokenTransactionStatus_TOKEN_TRANSACTION_STARTED
	case schema.TokenTransactionStatusStartedCancelled:
		return pb.TokenTransactionStatus_TOKEN_TRANSACTION_STARTED_CANCELLED
	case schema.TokenTransactionStatusSigned:
		return pb.TokenTransactionStatus_TOKEN_TRANSACTION_SIGNED
	case schema.TokenTransactionStatusSignedCancelled:
		return pb.TokenTransactionStatus_TOKEN_TRANSACTION_SIGNED_CANCELLED
	case schema.TokenTransactionStatusFinalized:
		return pb.TokenTransactionStatus_TOKEN_TRANSACTION_FINALIZED
	default:
		return pb.TokenTransactionStatus_TOKEN_TRANSACTION_UNKNOWN
	}
}
