package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
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

// The TokenTransactionHandler is responsible for handling token transaction requests to spend and create leaves.
type TokenTransactionHandler struct {
	config authz.Config
	db     *ent.Client
}

// NewTokenTransactionHandler creates a new TokenTransactionHandler.
func NewTokenTransactionHandler(config authz.Config, db *ent.Client) *TokenTransactionHandler {
	return &TokenTransactionHandler{
		config: config,
		db:     db,
	}
}

// StartTokenTransaction verifies the token leaves, reserves the keyshares for the token transaction, and returns metadata about the operators that possess the keyshares.
func (o TokenTransactionHandler) StartTokenTransaction(ctx context.Context, config *so.Config, req *pb.StartTokenTransactionRequest) (*pb.StartTokenTransactionResponse, error) {
	logger := helper.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, fmt.Errorf("identity public key authentication failed: %w", err)
	}

	if err := utils.ValidatePartialTokenTransaction(req.PartialTokenTransaction, req.TokenTransactionSignatures, config.GetSigningOperatorList(), config.SupportedNetworks); err != nil {
		return nil, fmt.Errorf("invalid partial token transaction: %w", err)
	}

	// Each created leaf requires a keyshare for revocation key generation.
	numRevocationKeysharesNeeded := len(req.PartialTokenTransaction.OutputLeaves)
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, o.db, config, numRevocationKeysharesNeeded)
	if err != nil {
		return nil, fmt.Errorf("failed to get unused signing keyshares: %w", err)
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}

	// Fill revocation public keys and withdrawal bond/locktime for each leaf.
	finalTokenTransaction := req.PartialTokenTransaction
	for i, leaf := range finalTokenTransaction.OutputLeaves {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		idStr := id.String()
		leaf.Id = &idStr
		leaf.RevocationPublicKey = keyshares[i].PublicKey
	}

	// Save the token transaction object to lock in the revocation public keys for each created leaf within this transaction.
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

		// Fill revocation public keys and withdrawal bond/locktime for each leaf.
		finalTokenTransaction := req.PartialTokenTransaction
		for i, leaf := range finalTokenTransaction.OutputLeaves {
			leafID := uuid.New().String()
			leaf.Id = &leafID
			leaf.RevocationPublicKey = keyshares[i].PublicKey
			// TODO: Support non-regtest configs by providing network as a field in the transaction.
			withdrawalBondSats := config.Lrc20Configs[network.String()].WithdrawBondSats
			leaf.WithdrawBondSats = &withdrawalBondSats
			withdrawRelativeBlockLocktime := config.Lrc20Configs[network.String()].WithdrawRelativeBlockLocktime
			leaf.WithdrawRelativeBlockLocktime = &withdrawRelativeBlockLocktime
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
func validateOutputs(leaves []*ent.TokenOutput, expectedStatus schema.TokenOutputStatus) []string {
	var invalidLeaves []string
	for i, leaf := range leaves {
		if leaf.Status != expectedStatus {
			invalidLeaves = append(invalidLeaves, fmt.Sprintf("output leaf %d has invalid status %s, expected %s",
				i, leaf.Status, expectedStatus))
		}
	}
	return invalidLeaves
}

// validateInputs checks if all spent outputs have the expected status and aren't withdrawn
func validateInputs(leaves []*ent.TokenOutput, expectedStatus schema.TokenOutputStatus) []string {
	var invalidLeaves []string
	for _, leaf := range leaves {
		if leaf.Status != expectedStatus {
			invalidLeaves = append(invalidLeaves, fmt.Sprintf("input leaf %x has invalid status %s, expected %s",
				leaf.ID, leaf.Status, expectedStatus))
		}
		if leaf.ConfirmedWithdrawBlockHash != nil {
			invalidLeaves = append(invalidLeaves, fmt.Sprintf("input leaf %x is already withdrawn",
				leaf.ID))
		}
	}
	return invalidLeaves
}

// SignTokenTransaction signs the token transaction with the operators private key.
// If it is a transfer it also fetches this operators keyshare for each spent leaf and
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

	// Validate each leaf signature in the request. Each signed payload consists of
	//   payload.final_token_transaction_hash
	//   payload.operator_identity_public_key
	// This verifies that this request for this transaction (and release of the revocation
	// keyshare) came from the leaf owner and was intended for this specific SO.
	for _, leafSig := range req.OperatorSpecificSignatures {
		payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(leafSig.Payload)
		if err != nil {
			return nil, fmt.Errorf("failed to hash revocation keyshares payload: %w", err)
		}

		// Validate that the transaction hash in the payload matches the actual transaction hash
		if !bytes.Equal(leafSig.Payload.FinalTokenTransactionHash, finalTokenTransactionHash) {
			return nil, fmt.Errorf("transaction hash in payload (%x) does not match actual transaction hash (%x)",
				leafSig.Payload.FinalTokenTransactionHash, finalTokenTransactionHash)
		}

		if err := utils.ValidateOwnershipSignature(
			leafSig.OwnerSignature,
			payloadHash,
			leafSig.OwnerPublicKey,
		); err != nil {
			return nil, fmt.Errorf("invalid owner signature for leaf: %w", err)
		}
	}
	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		logger.Info("Sign request for token transaction did not map to a previously started transaction", "error", err)
		return nil, fmt.Errorf("token transaction not found or could not be locked for signing: %w", err)
	}

	if tokenTransaction.Status == schema.TokenTransactionStatusSigned {
		return o.regenerateSigningResponseForDuplicateRequest(ctx, logger, config, tokenTransaction, finalTokenTransactionHash)
	}

	invalidLeaves := validateOutputs(tokenTransaction.Edges.CreatedOutput, schema.TokenOutputStatusCreatedStarted)
	if len(invalidLeaves) > 0 {
		return nil, fmt.Errorf("found invalid output leaves: %s", strings.Join(invalidLeaves, "; "))
	}

	// If token outputs are being spent, verify the expected status of inputs and check for active freezes.
	// For mints this is not necessary and will be skipped because it does not spend outputs.
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidLeaves := validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentStarted)
		if len(invalidLeaves) > 0 {
			return nil, fmt.Errorf("found invalid input leaves: %s", strings.Join(invalidLeaves, "; "))
		}

		// Collect owner public keys for freeze check
		ownerPublicKeys := make([][]byte, len(tokenTransaction.Edges.SpentOutput))
		// Assumes that all token public keys are the same as the first leaf. This is asserted when validating
		// in the StartTokenTransaction() step.
		tokenPublicKey := tokenTransaction.Edges.SpentOutput[0].TokenPublicKey
		for i, leaf := range tokenTransaction.Edges.SpentOutput {
			ownerPublicKeys[i] = leaf.OwnerPublicKey
		}

		// Bulk query all input leaf ids to ensure none of them are frozen.
		activeFreezes, err := ent.GetActiveFreezes(ctx, ownerPublicKeys, tokenPublicKey)
		if err != nil {
			logger.Info("Failed to query token freeze status", "error", err)
			return nil, fmt.Errorf("failed to query token freeze status: %w", err)
		}

		if len(activeFreezes) > 0 {
			for _, freeze := range activeFreezes {
				logger.Info("Found active freeze", "owner", freeze.OwnerPublicKey, "token", freeze.TokenPublicKey, "freeze_timestamp", freeze.WalletProvidedFreezeTimestamp)
			}
			return nil, fmt.Errorf("at least one input leaf is frozen. Cannot proceed with transaction")
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
		logger.Info("Failed to update leaves after signing", "error", err)
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
	for _, leaf := range tokenTransaction.Edges.SpentOutput {
		keyshare, err := leaf.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			logger.Info("Failed to get keyshare for leaf", "error", err)
			return nil, err
		}
		// Use the vout index to order the keyshares
		keyshares[leaf.SpentTransactionInputVout] = keyshare

		// Validate that the keyshare's public key is as expected.
		if !bytes.Equal(keyshare.PublicKey, leaf.WithdrawRevocationCommitment) {
			return nil, fmt.Errorf(
				"keyshare public key %x does not match leaf revocation public key %x",
				keyshare.PublicKey,
				leaf.WithdrawRevocationCommitment,
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

	var invalidLeaves []string
	isMint := tokenTransaction.Edges.Mint != nil
	expectedCreatedLeafStatus := schema.TokenOutputStatusCreatedSigned
	if isMint {
		expectedCreatedLeafStatus = schema.TokenOutputStatusCreatedFinalized
	}

	invalidLeaves = validateOutputs(tokenTransaction.Edges.CreatedOutput, expectedCreatedLeafStatus)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidLeaves = append(invalidLeaves, validateInputs(tokenTransaction.Edges.SpentOutput, schema.TokenOutputStatusSpentSigned)...)
	}
	if len(invalidLeaves) > 0 {
		return nil, fmt.Errorf("found invalid leaves when regenerating duplicate signing request: %s", strings.Join(invalidLeaves, "; "))
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

// FinalizeTokenTransaction takes the revocation private keys for spent leaves and updates their status to finalized.
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
		logger.Info("Failed to fetch matching transaction receipt", "error", err)
		return nil, fmt.Errorf("failed to fetch transaction receipt: %w", err)
	}

	// Verify that the transaction is in a signed state before finalizing
	if tokenTransaction.Status != schema.TokenTransactionStatusSigned {
		return nil, fmt.Errorf("transaction is in status %s, but must be in SIGNED status to finalize", tokenTransaction.Status)
	}

	// Verify status of output leaves
	var invalidLeaves []string
	for i, leaf := range tokenTransaction.Edges.CreatedOutput {
		if leaf.Status != schema.TokenOutputStatusCreatedSigned {
			invalidLeaves = append(invalidLeaves, fmt.Sprintf("output leaf %d has invalid status %s, expected %s", i, leaf.Status, schema.TokenOutputStatusCreatedSigned))
		}
	}

	// Verify status of spent leaves
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		for _, leaf := range tokenTransaction.Edges.SpentOutput {
			if leaf.Status != schema.TokenOutputStatusSpentSigned {
				invalidLeaves = append(invalidLeaves, fmt.Sprintf("input leaf %x has invalid status %s, expected %s",
					leaf.ID, leaf.Status, schema.TokenOutputStatusSpentSigned))
			}
			if leaf.ConfirmedWithdrawBlockHash != nil {
				invalidLeaves = append(invalidLeaves, fmt.Sprintf("input leaf %x is already withdrawn",
					leaf.ID))
			}
		}
	}

	if len(invalidLeaves) > 0 {
		return nil, fmt.Errorf("found invalid leaves: %s", strings.Join(invalidLeaves, "; "))
	}

	// Extract revocation public keys from spent leaves
	revocationPublicKeys := make([][]byte, len(tokenTransaction.Edges.SpentOutput))
	if (len(tokenTransaction.Edges.SpentOutput)) != len(req.LeafToSpendRevocationKeys) {
		return nil, fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent leaves (%d)",
			len(req.LeafToSpendRevocationKeys),
			len(tokenTransaction.Edges.SpentOutput),
		)
	}
	for _, leaf := range tokenTransaction.Edges.SpentOutput {
		revocationPublicKeys[leaf.SpentTransactionInputVout] = leaf.WithdrawRevocationCommitment
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
		tokenTransaction.OperatorSignature,
		req.LeafToSpendRevocationKeys)
	if err != nil {
		logger.Info("Failed to send transaction to LRC20 node", "error", err)
		return nil, err
	}

	err = ent.UpdateFinalizedTransaction(ctx, tokenTransaction, req.LeafToSpendRevocationKeys)
	if err != nil {
		logger.Info("Failed to update leaves after finalizing", "error", err)
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

	lrc20Client := lrc20.NewClient(config)
	err := lrc20Client.SendSparkSignature(ctx, signatureData)
	if err != nil {
		return fmt.Errorf("failed to send spark signature to LRC20 node: %w", err)
	}
	return nil
}

// FreezeTokens freezes or unfreezes tokens on the LRC20 node.
func (o TokenTransactionHandler) FreezeTokens(
	ctx context.Context,
	config *so.Config,
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

	// Collect information about the frozen leaves.
	leafIDs, totalAmount, err := ent.GetOwnedLeafTokenStats(ctx, [][]byte{req.FreezeTokensPayload.OwnerPublicKey}, req.FreezeTokensPayload.TokenPublicKey)
	if err != nil {
		logger.Info("Failed to get impacted leaf stats", "error", err)
		return nil, err
	}

	err = o.FreezeTokensOnLRC20Node(ctx, config, req)
	if err != nil {
		logger.Info("Failed to freeze tokens on LRC20 node", "error", err)
		return nil, fmt.Errorf("failed to freeze tokens on LRC20 node: %w", err)
	}

	return &pb.FreezeTokensResponse{
		ImpactedLeafIds:     leafIDs,
		ImpactedTokenAmount: totalAmount.Bytes(),
	}, nil
}

// FreezeTokensOnLRC20Node freezes or unfreezes tokens on the LRC20 node.
func (o TokenTransactionHandler) FreezeTokensOnLRC20Node(
	ctx context.Context,
	config *so.Config,
	req *pb.FreezeTokensRequest,
) error {
	lrc20Client := lrc20.NewClient(config)
	return lrc20Client.FreezeTokens(ctx, req)
}

// QueryTokenTransactions returns SO provided data about specific token transactions along with their status.
// Allows caller to specify data to be returned related to:
// a) transactions associated with a particular set of leaf ids
// b) transactions associated with a particular set of transaction hashes
// c) all transactions associated with a particular token public key
func (o TokenTransactionHandler) QueryTokenTransactions(ctx context.Context, config *so.Config, req *pb.QueryTokenTransactionsRequest) (*pb.QueryTokenTransactionsResponse, error) {
	logger := helper.GetLoggerFromContext(ctx)
	db := ent.GetDbFromContext(ctx)

	// Start with a base query for token transaction receipts
	baseQuery := db.TokenTransaction.Query()

	// Apply filters based on request parameters
	if len(req.LeafIds) > 0 {
		// Convert string IDs to UUIDs
		leafUUIDs := make([]uuid.UUID, 0, len(req.LeafIds))
		for _, idStr := range req.LeafIds {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return nil, fmt.Errorf("invalid leaf ID format: %v", err)
			}
			leafUUIDs = append(leafUUIDs, id)
		}

		// Find transactions that created or spent these leaves
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.IDIn(leafUUIDs...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.IDIn(leafUUIDs...)),
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
	receipts, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query token transactions: %v", err)
	}

	// Convert to response protos
	transactionsWithStatus := make([]*pb.TokenTransactionWithStatus, 0, len(receipts))
	for _, tokenTransaction := range receipts {
		// Determine transaction status based on leaf statuses
		status := pb.TokenTransactionStatus_TOKEN_TRANSACTION_STARTED

		// Check spent leaves status
		spentLeafStatuses := make(map[schema.TokenOutputStatus]int)

		for _, leaf := range tokenTransaction.Edges.SpentOutput {
			// Verify that this spent leaf is actually associated with this transaction
			if leaf.Edges.OutputSpentTokenTransaction == nil ||
				leaf.Edges.OutputSpentTokenTransaction.ID != tokenTransaction.ID {
				logger.Info("Warning: Spent leaf not properly associated with transaction", "leaf_id", leaf.ID.String(), "transaction_id", tokenTransaction.ID.String())
				continue
			}
			spentLeafStatuses[leaf.Status]++
		}

		// Reconstruct the token transaction from the receipt data
		tokenTransaction, err := tokenTransaction.MarshalProto(config)
		if err != nil {
			logger.Info("Failed to marshal token transaction", "error", err)
			return nil, err
		}

		// This would require reconstructing the transaction from the database
		// For now, we'll just include the transaction hash

		transactionsWithStatus = append(transactionsWithStatus, &pb.TokenTransactionWithStatus{
			TokenTransaction: tokenTransaction,
			Status:           status,
		})
	}

	// Calculate next offset
	var nextOffset int64
	if len(receipts) == int(req.Limit) {
		nextOffset = req.Offset + int64(len(receipts))
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

	leaves, err := ent.GetOwnedLeaves(ctx, req.OwnerPublicKeys, req.TokenPublicKeys)
	if err != nil {
		logger.Info("Failed to get owned leaf stats", "error", err)
		return nil, err
	}

	leavesWithPrevTxData := make([]*pb.LeafWithPreviousTransactionData, len(leaves))
	for i, leaf := range leaves {
		idStr := leaf.ID.String()
		leavesWithPrevTxData[i] = &pb.LeafWithPreviousTransactionData{
			Leaf: &pb.TokenLeafOutput{
				Id:                            &idStr,
				OwnerPublicKey:                leaf.OwnerPublicKey,
				RevocationPublicKey:           leaf.WithdrawRevocationCommitment,
				WithdrawBondSats:              &leaf.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: &leaf.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                leaf.TokenPublicKey,
				TokenAmount:                   leaf.TokenAmount,
			},
			PreviousTransactionHash: leaf.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
			PreviousTransactionVout: uint32(leaf.CreatedTransactionOutputVout),
		}
	}

	return &pb.QueryTokenOutputsResponse{
		LeavesWithPreviousTransactionData: leavesWithPrevTxData,
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
		logger.Info("Failed to fetch matching transaction receipt", "error", err)
		return nil, fmt.Errorf("failed to fetch transaction receipt: %w", err)
	}

	// Verify that the transaction is in a signed state locally
	if tokenTransaction.Status != schema.TokenTransactionStatusSigned {
		return nil, fmt.Errorf("transaction is in status %s, but must be in SIGNED status to cancel", tokenTransaction.Status)
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
		logger.Info("Failed to update leaves after canceling", "error", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (o TokenTransactionHandler) getRevocationKeysharesForTokenTransaction(ctx context.Context, tokenTransaction *ent.TokenTransaction) ([][]byte, error) {
	logger := helper.GetLoggerFromContext(ctx)

	keyshares := make([]*ent.SigningKeyshare, len(tokenTransaction.Edges.SpentOutput))
	for _, leaf := range tokenTransaction.Edges.SpentOutput {
		keyshare, err := leaf.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			logger.Info("Failed to get keyshare for leaf",
				"error", err,
				"output_id", leaf.ID,
				"transaction_hash", hex.EncodeToString(tokenTransaction.FinalizedTokenTransactionHash))
			return nil, err
		}
		// Use the vout index to order the keyshares
		keyshares[leaf.SpentTransactionInputVout] = keyshare

		// Validate that the keyshare's public key is as expected.
		if !bytes.Equal(keyshare.PublicKey, leaf.WithdrawRevocationCommitment) {
			return nil, fmt.Errorf(
				"keyshare public key %x does not match leaf revocation public key %x",
				keyshare.PublicKey,
				leaf.WithdrawRevocationCommitment,
			)
		}
	}

	revocationKeyshares := make([][]byte, len(keyshares))
	for i, keyshare := range keyshares {
		revocationKeyshares[i] = keyshare.SecretShare
	}
	return revocationKeyshares, nil
}
