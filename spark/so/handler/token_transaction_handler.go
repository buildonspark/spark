package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"

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
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark-go/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/lrc20"
	"github.com/lightsparkdev/spark-go/so/utils"
	"google.golang.org/protobuf/types/known/emptypb"
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
	logger := helper.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, fmt.Errorf("identity public key authentication failed: %w", err)
	}

	if err := utils.ValidatePartialTokenTransaction(req.PartialTokenTransaction, req.TokenTransactionSignatures, config.GetSigningOperatorList(), config.SupportedNetworks); err != nil {
		return nil, fmt.Errorf("invalid partial token transaction: %w", err)
	}

	// Each created output requires a keyshare for revocation key generation.
	numRevocationKeysharesNeeded := len(req.PartialTokenTransaction.TokenOutputs)
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, o.db, config, numRevocationKeysharesNeeded)
	if err != nil {
		return nil, fmt.Errorf("failed to get unused signing keyshares: %w", err)
	}

	if len(keyshares) < numRevocationKeysharesNeeded {
		return nil, fmt.Errorf("not enough unused signing keyshares available: %d needed, %d available", numRevocationKeysharesNeeded, len(keyshares))
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
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
	}

	// Save the token transaction object to lock in the revocation commitments for each created output within this transaction.
	// Note that atomicity here is very important to ensure that the unused keyshares queried above are not used by another operation.
	// This property should be help because the coordinator blocks on the other SO responses.
	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			logger.Info("Failed to connect to operator for marking token transaction keyshare", "error", err)
			return nil, fmt.Errorf("failed to connect to operator %s: %w", operator.Identifier, err)
		}
		defer conn.Close()

		network, err := common.NetworkFromProtoNetwork(req.PartialTokenTransaction.Network)
		if err != nil {
			logger.Info("Failed to get network from proto network", "error", err)
			return nil, fmt.Errorf("failed to get network from proto network: %w", err)
		}

		// Fill revocation commitments and withdrawal bond/locktime for each output.
		finalTokenTransaction := req.PartialTokenTransaction
		for i, output := range finalTokenTransaction.TokenOutputs {
			outputID := uuid.New().String()
			output.Id = &outputID
			output.RevocationCommitment = keyshares[i].PublicKey
			withdrawalBondSats := config.Lrc20Configs[network.String()].WithdrawBondSats
			output.WithdrawBondSats = &withdrawalBondSats
			withdrawRelativeBlockLocktime := config.Lrc20Configs[network.String()].WithdrawRelativeBlockLocktime
			output.WithdrawRelativeBlockLocktime = &withdrawRelativeBlockLocktime
		}

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.StartTokenTransactionInternal(ctx, &pbinternal.StartTokenTransactionInternalRequest{
			KeyshareIds:                keyshareIDStrings,
			FinalTokenTransaction:      finalTokenTransaction,
			TokenTransactionSignatures: req.TokenTransactionSignatures,
		})
		if err != nil {
			logger.Info("Failed to execute start token transaction task with operator", "operator", operator.Identifier, "error", err)
			return nil, fmt.Errorf("failed to execute start token transaction with operator %s: %w", operator.Identifier, err)
		}
		return internalResp, err
	})
	if err != nil {
		logger.Info("Failed to successfully execute start token transaction task with all operators", "error", err)
		return nil, fmt.Errorf("failed to execute start token transaction with all operators: %w", err)
	}

	operatorList, err := allSelection.OperatorList(config)
	if err != nil {
		logger.Info("Failed to get selection operator list", "error", err)
		return nil, fmt.Errorf("failed to get operator list: %w", err)
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

// validateOperatorSpecificSignatures validates the signatures in the request against the transaction hash
// and verifies that the number of signatures matches the expected count based on transaction type
func validateOperatorSpecificSignatures(identityPublicKey []byte, operatorSpecificSignatures []*pb.OperatorSpecificTokenTransactionSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		return validateTransferOperatorSpecificSignatures(identityPublicKey, operatorSpecificSignatures, tokenTransaction)
	}
	return validateMintOperatorSpecificSignatures(identityPublicKey, operatorSpecificSignatures, tokenTransaction)
}

// validateTransferOperatorSpecificSignatures validates signatures for transfer transactions
func validateTransferOperatorSpecificSignatures(identityPublicKey []byte, operatorSpecificSignatures []*pb.OperatorSpecificTokenTransactionSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(operatorSpecificSignatures) != len(tokenTransaction.Edges.SpentOutput) {
		return fmt.Errorf("expected %d signatures for transfer (one per input), but got %d (transaction_uuid: %s, transaction_hash: %x)",
			len(tokenTransaction.Edges.SpentOutput), len(operatorSpecificSignatures),
			tokenTransaction.ID.String(), tokenTransaction.FinalizedTokenTransactionHash)
	}

	spentOutputs := make([]*ent.TokenOutput, len(tokenTransaction.Edges.SpentOutput))
	copy(spentOutputs, tokenTransaction.Edges.SpentOutput)
	sort.Slice(spentOutputs, func(i, j int) bool {
		return spentOutputs[i].SpentTransactionInputVout < spentOutputs[j].SpentTransactionInputVout
	})

	for i, outputSig := range operatorSpecificSignatures {
		spentOutput := spentOutputs[i]
		if !bytes.Equal(outputSig.OwnerPublicKey, spentOutput.OwnerPublicKey) {
			return fmt.Errorf("owner public key mismatch for input %d: signature has %x but database record has %x (transaction_uuid: %s, transaction_hash: %x)",
				i, outputSig.OwnerPublicKey, spentOutput.OwnerPublicKey,
				tokenTransaction.ID.String(), tokenTransaction.FinalizedTokenTransactionHash)
		}

		if err := validateSignaturePayload(identityPublicKey, outputSig, tokenTransaction); err != nil {
			return fmt.Errorf("%w (transaction_uuid: %s, transaction_hash: %x)",
				err, tokenTransaction.ID.String(), tokenTransaction.FinalizedTokenTransactionHash)
		}
	}

	return nil
}

// validateMintOperatorSpecificSignatures validates signatures for mint transactions
func validateMintOperatorSpecificSignatures(identityPublicKey []byte, operatorSpecificSignatures []*pb.OperatorSpecificTokenTransactionSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(operatorSpecificSignatures) != 1 {
		return fmt.Errorf("expected exactly 1 signature for mint, but got %d (transaction_uuid: %s, transaction_hash: %x)",
			len(operatorSpecificSignatures), tokenTransaction.ID.String(), tokenTransaction.FinalizedTokenTransactionHash)
	}

	if tokenTransaction.Edges.Mint == nil {
		return fmt.Errorf("mint record not found in db, but expected a mint for this transaction (transaction_uuid: %s, transaction_hash: %x)",
			tokenTransaction.ID.String(), tokenTransaction.FinalizedTokenTransactionHash)
	}

	if !bytes.Equal(operatorSpecificSignatures[0].OwnerPublicKey, tokenTransaction.Edges.Mint.IssuerPublicKey) {
		return fmt.Errorf("owner public key in signature (%x) does not match issuer public key in mint (%x) (transaction_uuid: %s, transaction_hash: %x)",
			operatorSpecificSignatures[0].OwnerPublicKey, tokenTransaction.Edges.Mint.IssuerPublicKey,
			tokenTransaction.ID.String(), tokenTransaction.FinalizedTokenTransactionHash)
	}

	if err := validateSignaturePayload(identityPublicKey, operatorSpecificSignatures[0], tokenTransaction); err != nil {
		return fmt.Errorf("%w (transaction_uuid: %s, transaction_hash: %x)",
			err, tokenTransaction.ID.String(), tokenTransaction.FinalizedTokenTransactionHash)
	}

	return nil
}

// validateSignaturePayload validates the payload and signature of an operator-specific signature
func validateSignaturePayload(identityPublicKey []byte, outputSig *pb.OperatorSpecificTokenTransactionSignature, tokenTransaction *ent.TokenTransaction) error {
	payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(outputSig.Payload)
	if err != nil {
		return fmt.Errorf("failed to hash revocation keyshares payload: %w", err)
	}

	if !bytes.Equal(outputSig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash) {
		return fmt.Errorf("transaction hash in payload (%x) does not match actual transaction hash (%x)",
			outputSig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash)
	}

	if len(outputSig.Payload.OperatorIdentityPublicKey) > 0 {
		if !bytes.Equal(outputSig.Payload.OperatorIdentityPublicKey, identityPublicKey) {
			return fmt.Errorf("operator identity public key in payload (%x) does not match this SO's identity public key (%x) (transaction_uuid: %s)",
				outputSig.Payload.OperatorIdentityPublicKey, identityPublicKey, tokenTransaction.ID.String())
		}
	}

	if err := utils.ValidateOwnershipSignature(
		outputSig.OwnerSignature,
		payloadHash,
		outputSig.OwnerPublicKey,
	); err != nil {
		return fmt.Errorf("invalid owner signature for output: %w", err)
	}

	return nil
}

// SignTokenTransaction signs the token transaction with the operators private key.
// If it is a transfer it also fetches this operators keyshare for each spent output and
// returns it to the wallet so it can finalize the transaction.
func (o TokenTransactionHandler) SignTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.SignTokenTransactionRequest,
) (*pb.SignTokenTransactionResponse, error) {
	logger := helper.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	finalTokenTransactionHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	if err != nil {
		logger.Info("Failed to hash final token transaction", "error", err, "transaction", req.FinalTokenTransaction)
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		logger.Info("Sign request for token transaction did not map to a previously started transaction", "error", err)
		return nil, fmt.Errorf("token transaction not found or could not be locked for signing: %w", err)
	}

	if err := validateOperatorSpecificSignatures(config.IdentityPublicKey(), req.OperatorSpecificSignatures, tokenTransaction); err != nil {
		return nil, err
	}

	if tokenTransaction.Status == schema.TokenTransactionStatusSigned {
		return o.regenerateSigningResponseForDuplicateRequest(ctx, logger, config, tokenTransaction, finalTokenTransactionHash)
	}

	invalidOutputs := validateOutputs(tokenTransaction.Edges.CreatedOutput, schema.TokenOutputStatusCreatedStarted)
	if len(invalidOutputs) > 0 {
		return nil, fmt.Errorf("found invalid outputs: %s", strings.Join(invalidOutputs, "; "))
	}

	// If token outputs are being spent, verify the expected status of inputs and check for active freezes.
	// For mints this is not necessary and will be skipped because it does not spend outputs.
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs := validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentStarted)
		if len(invalidOutputs) > 0 {
			return nil, fmt.Errorf("found invalid inputs: %s", strings.Join(invalidOutputs, "; "))
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
			logger.Info("Failed to query token freeze status", "error", err)
			return nil, fmt.Errorf("failed to query token freeze status: %w", err)
		}

		if len(activeFreezes) > 0 {
			for _, freeze := range activeFreezes {
				logger.Info("Found active freeze", "owner", freeze.OwnerPublicKey, "token", freeze.TokenPublicKey, "freeze_timestamp", freeze.WalletProvidedFreezeTimestamp)
			}
			return nil, fmt.Errorf("at least one input is frozen. Cannot proceed with transaction")
		}
	}

	// Sign the token transaction hash with the operator identity private key.
	identityPrivateKey := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey)
	operatorSignature := ecdsa.Sign(identityPrivateKey, finalTokenTransactionHash)

	operatorSpecificSignature := make([][]byte, len(req.OperatorSpecificSignatures))
	for i, sig := range req.OperatorSpecificSignatures {
		operatorSpecificSignature[i] = sig.OwnerSignature
	}
	err = ent.UpdateSignedTransaction(ctx, tokenTransaction, operatorSpecificSignature, operatorSignature.Serialize())
	if err != nil {
		logger.Info("Failed to update outputs after signing", "error", err)
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
		logger.Info("Failed to send transaction to LRC20 node", "error", err)
		return nil, fmt.Errorf("failed to send transaction to LRC20 node: %w", err)
	}

	keyshares := make([]*ent.SigningKeyshare, len(tokenTransaction.Edges.SpentOutput))
	for _, output := range tokenTransaction.Edges.SpentOutput {
		keyshare, err := output.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			logger.Info("Failed to get keyshare for output", "error", err)
			return nil, err
		}
		// Use the vout index to order the keyshares
		keyshares[output.SpentTransactionInputVout] = keyshare

		// Validate that the keyshare's public key is as expected.
		if !bytes.Equal(keyshare.PublicKey, output.WithdrawRevocationCommitment) {
			return nil, fmt.Errorf(
				"keyshare public key %x does not match output revocation commitment %x",
				keyshare.PublicKey,
				output.WithdrawRevocationCommitment,
			)
		}
	}

	revocationKeyshares, err := o.getRevocationKeysharesForTokenTransaction(ctx, tokenTransaction)
	if err != nil {
		logger.Info("Failed to get revocation keyshares",
			"error", err,
			"transaction_hash", hex.EncodeToString(finalTokenTransactionHash))
		return nil, fmt.Errorf("failed to get revocation keyshares for transaction %x: %w", finalTokenTransactionHash, err)
	}
	return &pb.SignTokenTransactionResponse{
		SparkOperatorSignature:              operatorSignature.Serialize(),
		TokenTransactionRevocationKeyshares: revocationKeyshares,
	}, nil
}

// regenerateSigningResponseForDuplicateRequest handles the case where a transaction has already been signed.
// This allows for simpler wallet SDK logic such that if a Sign() call to one of the SOs failed,
// the wallet SDK can retry with all SOs and get successful responses.
func (o TokenTransactionHandler) regenerateSigningResponseForDuplicateRequest(
	ctx context.Context,
	logger *slog.Logger,
	config *so.Config,
	tokenTransaction *ent.TokenTransaction,
	finalTokenTransactionHash []byte,
) (*pb.SignTokenTransactionResponse, error) {
	logger.Info("Regenerating response for a duplicate SignTokenTransaction() Call")

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
		return nil, fmt.Errorf("found invalid outputs when regenerating duplicate signing request: %s", strings.Join(invalidOutputs, "; "))
	}

	if err := utils.ValidateOwnershipSignature(
		tokenTransaction.OperatorSignature,
		finalTokenTransactionHash,
		config.IdentityPublicKey(),
	); err != nil {
		logger.Info("Stored operator signature is invalid", "error", err)
		return nil, fmt.Errorf("stored operator signature is invalid: %w", err)
	}

	revocationKeyshares, err := o.getRevocationKeysharesForTokenTransaction(ctx, tokenTransaction)
	if err != nil {
		logger.Info("Failed to get revocation keyshares",
			"error", err,
			"transaction_hash", hex.EncodeToString(finalTokenTransactionHash))
		return nil, err
	}
	logger.Debug("Returning stored signature in response to repeat Sign() call")
	return &pb.SignTokenTransactionResponse{
		SparkOperatorSignature:              tokenTransaction.OperatorSignature,
		TokenTransactionRevocationKeyshares: revocationKeyshares,
	}, nil
}

// FinalizeTokenTransaction takes the revocation private keys for spent outputs and updates their status to finalized.
func (o TokenTransactionHandler) FinalizeTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.FinalizeTokenTransactionRequest,
) (*emptypb.Empty, error) {
	logger := helper.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		logger.Info("Failed to fetch matching transaction", "error", err)
		return nil, fmt.Errorf("failed to fetch transaction: %w", err)
	}

	// Verify that the transaction is in a signed state before finalizing
	if tokenTransaction.Status != schema.TokenTransactionStatusSigned {
		return nil, fmt.Errorf("transaction is in status %s, but must be in SIGNED status to finalize", tokenTransaction.Status)
	}

	// Verify status of created outputs and spent outputs
	invalidOutputs := validateOutputs(tokenTransaction.Edges.CreatedOutput, schema.TokenOutputStatusCreatedSigned)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentSigned)...)
	}

	if len(invalidOutputs) > 0 {
		return nil, fmt.Errorf("found invalid outputs: %s", strings.Join(invalidOutputs, "; "))
	}

	revocationPrivateKeys := make([]*secp256k1.PrivateKey, len(req.OutputToSpendRevocationSecrets))
	for i, revocationSecret := range req.OutputToSpendRevocationSecrets {
		revocationPrivateKey, err := common.PrivateKeyFromBytes(revocationSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to parse revocation private key: %w", err)
		}

		revocationPrivateKeys[i] = revocationPrivateKey
	}

	// Extract revocation commitments from spent outputs.
	revocationPublicKeys := make([][]byte, len(tokenTransaction.Edges.SpentOutput))
	if (len(tokenTransaction.Edges.SpentOutput)) != len(req.OutputToSpendRevocationSecrets) {
		return nil, fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent outputs (%d)",
			len(req.OutputToSpendRevocationSecrets),
			len(tokenTransaction.Edges.SpentOutput),
		)
	}
	for _, output := range tokenTransaction.Edges.SpentOutput {
		revocationPublicKeys[output.SpentTransactionInputVout] = output.WithdrawRevocationCommitment
	}
	err = utils.ValidateRevocationKeys(revocationPrivateKeys, revocationPublicKeys)
	if err != nil {
		return nil, err
	}

	err = o.SendTransactionToLRC20Node(
		ctx,
		config,
		req.FinalTokenTransaction,
		// TODO: Consider removing this because it was already provided in the Sign() step.
		tokenTransaction.OperatorSignature,
		req.OutputToSpendRevocationSecrets)
	if err != nil {
		logger.Info("Failed to send transaction to LRC20 node", "error", err)
		return nil, err
	}

	err = ent.UpdateFinalizedTransaction(ctx, tokenTransaction, req.OutputToSpendRevocationSecrets)
	if err != nil {
		logger.Info("Failed to update outputs after finalizing", "error", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

// SendTransactionToLRC20Node sends a token transaction to the LRC20 node.
func (o TokenTransactionHandler) SendTransactionToLRC20Node(
	ctx context.Context,
	config *so.Config,
	finalTokenTransaction *pb.TokenTransaction,
	operatorSignature []byte,
	revocationKeys [][]byte,
) error {
	outputsToSpendData := make([]*pblrc20.SparkSignatureOutputData, len(revocationKeys))
	for i, revocationKey := range revocationKeys {
		outputsToSpendData[i] = &pblrc20.SparkSignatureOutputData{
			SpentOutputIndex: uint32(i),
			// Revocation will be nil if we are sending the transaction at the Sign() step.
			// It will be filled when sending a transaction in the Finalize() step.
			RevocationPrivateKey: revocationKey,
		}
	}

	signatureData := &pblrc20.SparkSignatureData{
		SparkOperatorSignature:         operatorSignature,
		SparkOperatorIdentityPublicKey: config.IdentityPublicKey(),
		FinalTokenTransaction:          finalTokenTransaction,
		OutputsToSpendData:             outputsToSpendData,
	}

	return o.lrc20Client.SendSparkSignature(ctx, signatureData)
}

// FreezeTokens freezes or unfreezes tokens on the LRC20 node.
func (o TokenTransactionHandler) FreezeTokens(
	ctx context.Context,
	req *pb.FreezeTokensRequest,
) (*pb.FreezeTokensResponse, error) {
	logger := helper.GetLoggerFromContext(ctx)

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
		logger.Info("Failed to query token freeze status", "error", err)
		return nil, fmt.Errorf("failed to query token freeze status: %w", err)
	}
	if req.FreezeTokensPayload.ShouldUnfreeze {
		if len(activeFreezes) == 0 {
			return nil, fmt.Errorf("no active freezes found to thaw")
		}
		if len(activeFreezes) > 1 {
			return nil, fmt.Errorf("multiple active freezes found for this owner and token which should not happen")
		}
		err = ent.ThawActiveFreeze(ctx, activeFreezes[0].ID, req.FreezeTokensPayload.IssuerProvidedTimestamp)
		if err != nil {
			logger.Info("Failed to update token freeze status to thawed", "error", err)
			return nil, err
		}
	} else { // Freeze
		if len(activeFreezes) > 0 {
			return nil, fmt.Errorf("tokens are already frozen for this owner and token")
		}
		err = ent.ActivateFreeze(ctx,
			req.FreezeTokensPayload.OwnerPublicKey,
			req.FreezeTokensPayload.TokenPublicKey,
			req.IssuerSignature,
			req.FreezeTokensPayload.IssuerProvidedTimestamp,
		)
		if err != nil {
			logger.Info("Failed to create token freeze entity", "error", err)
			return nil, err
		}
	}

	// Collect information about the frozen outputs.
	outputIDs, totalAmount, err := ent.GetOwnedTokenOutputStats(ctx, [][]byte{req.FreezeTokensPayload.OwnerPublicKey}, req.FreezeTokensPayload.TokenPublicKey)
	if err != nil {
		logger.Info("Failed to get impacted output stats", "error", err)
		return nil, err
	}

	err = o.FreezeTokensOnLRC20Node(ctx, req)
	if err != nil {
		logger.Info("Failed to freeze tokens on LRC20 node", "error", err)
		return nil, fmt.Errorf("failed to freeze tokens on LRC20 node: %w", err)
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
	logger := helper.GetLoggerFromContext(ctx)
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
		status := pb.TokenTransactionStatus_TOKEN_TRANSACTION_STARTED

		// Check spent outputs status
		spentOutputStatuses := make(map[schema.TokenOutputStatus]int)

		for _, output := range transaction.Edges.SpentOutput {
			// Verify that this spent output is actually associated with this transaction.
			if output.Edges.OutputSpentTokenTransaction == nil ||
				output.Edges.OutputSpentTokenTransaction.ID != transaction.ID {
				logger.Info("Warning: Spent output not properly associated with transaction", "output_id", output.ID.String(), "transaction_uuid", transaction.ID.String())
				continue
			}
			spentOutputStatuses[output.Status]++
		}

		// Reconstruct the token transaction from the ent data.
		transaction, err := transaction.MarshalProto(config)
		if err != nil {
			logger.Info("Failed to marshal token transaction", "error", err)
			return nil, err
		}

		// This would require reconstructing the transaction from the database
		// For now, we'll just include the transaction hash.
		transactionsWithStatus = append(transactionsWithStatus, &pb.TokenTransactionWithStatus{
			TokenTransaction: transaction,
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
	req *pb.QueryTokenOutputsRequest,
) (*pb.QueryTokenOutputsResponse, error) {
	logger := helper.GetLoggerFromContext(ctx)

	outputs, err := ent.GetOwnedTokenOutputs(ctx, req.OwnerPublicKeys, req.TokenPublicKeys)
	if err != nil {
		logger.Info("Failed to get owned output stats", "error", err)
		return nil, err
	}

	outputsWithPrevTxData := make([]*pb.OutputWithPreviousTransactionData, len(outputs))
	for i, output := range outputs {
		idStr := output.ID.String()
		outputsWithPrevTxData[i] = &pb.OutputWithPreviousTransactionData{
			Output: &pb.TokenOutput{
				Id:                            &idStr,
				OwnerPublicKey:                output.OwnerPublicKey,
				RevocationCommitment:          output.WithdrawRevocationCommitment,
				WithdrawBondSats:              &output.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: &output.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                output.TokenPublicKey,
				TokenAmount:                   output.TokenAmount,
			},
			PreviousTransactionHash: output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
			PreviousTransactionVout: uint32(output.CreatedTransactionOutputVout),
		}
	}

	return &pb.QueryTokenOutputsResponse{
		OutputsWithPreviousTransactionData: outputsWithPrevTxData,
	}, nil
}

func (o TokenTransactionHandler) CancelSignedTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.CancelSignedTokenTransactionRequest,
) (*emptypb.Empty, error) {
	logger := helper.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.SenderIdentityPublicKey); err != nil {
		return nil, err
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		logger.Info("Failed to fetch matching transaction", "error", err)
		return nil, fmt.Errorf("failed to fetch transaction: %w", err)
	}

	// Verify that the transaction is in a signed state locally
	if tokenTransaction.Status != schema.TokenTransactionStatusSigned {
		return nil, fmt.Errorf("transaction is in status %s, but must be in %s status to cancel",
			tokenTransaction.Status, schema.TokenTransactionStatusSigned)
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
			logger.Info("Failed to connect to operator for validating transaction state before cancelling", "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pb.NewSparkServiceClient(conn)
		internalResp, err := client.QueryTokenTransactions(ctx, &pb.QueryTokenTransactionsRequest{
			TokenTransactionHashes: [][]byte{tokenTransaction.FinalizedTokenTransactionHash},
		})
		if err != nil {
			logger.Info("Failed to execute start token transaction task with operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		return internalResp, err
	})
	if err != nil {
		logger.Info("Failed to successfully execute start token transaction task with all operators", "error", err)
		return nil, err
	}

	// Check if any operator has finalized the transaction
	signedCount := 0
	for _, resp := range responses {
		queryResp, ok := resp.(*pb.QueryTokenTransactionsResponse)
		if !ok || queryResp == nil {
			return nil, fmt.Errorf("invalid response from operator")
		}

		for _, txWithStatus := range queryResp.TokenTransactionsWithStatus {
			if txWithStatus.Status == pb.TokenTransactionStatus_TOKEN_TRANSACTION_FINALIZED {
				return nil, fmt.Errorf("transaction has already been finalized by at least one operator, cannot cancel")
			}
			if txWithStatus.Status == pb.TokenTransactionStatus_TOKEN_TRANSACTION_SIGNED {
				signedCount++
			}
		}
	}

	// Check if too many operators have already signed
	operatorCount := len(config.GetSigningOperatorList())
	threshold := int(config.Threshold)
	if signedCount > operatorCount-threshold {
		return nil, fmt.Errorf("transaction has been signed by %d operators, which exceeds the cancellation threshold of %d",
			signedCount, operatorCount-threshold)
	}

	err = ent.UpdateCancelledTransaction(ctx, tokenTransaction)
	if err != nil {
		logger.Info("Failed to update outputs after canceling", "error", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (o TokenTransactionHandler) getRevocationKeysharesForTokenTransaction(ctx context.Context, tokenTransaction *ent.TokenTransaction) ([][]byte, error) {
	logger := helper.GetLoggerFromContext(ctx)

	keyshares := make([]*ent.SigningKeyshare, len(tokenTransaction.Edges.SpentOutput))
	for _, output := range tokenTransaction.Edges.SpentOutput {
		keyshare, err := output.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			logger.Info("Failed to get keyshare for output",
				"error", err,
				"output_id", output.ID,
				"transaction_hash", hex.EncodeToString(tokenTransaction.FinalizedTokenTransactionHash))
			return nil, err
		}
		// Use the vout index to order the keyshares
		keyshares[output.SpentTransactionInputVout] = keyshare

		// Validate that the keyshare's public key is as expected.
		if !bytes.Equal(keyshare.PublicKey, output.WithdrawRevocationCommitment) {
			return nil, fmt.Errorf(
				"keyshare public key %x does not match output revocation commitment %x",
				keyshare.PublicKey,
				output.WithdrawRevocationCommitment,
			)
		}
	}

	revocationKeyshares := make([][]byte, len(keyshares))
	for i, keyshare := range keyshares {
		revocationKeyshares[i] = keyshare.SecretShare
	}
	return revocationKeyshares, nil
}
