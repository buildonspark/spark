package tokens

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"

	"github.com/lightsparkdev/spark"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	sparkinternalpb "github.com/lightsparkdev/spark/proto/spark_internal"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
)

type StartTokenTransactionHandler struct {
	config           *so.Config
	enablePreemption bool
	prepareHandler   *InternalPrepareTokenHandler
}

// NewStartTokenTransactionHandler creates a new StartTokenTransactionHandler.
func NewStartTokenTransactionHandler(config *so.Config) *StartTokenTransactionHandler {
	return &StartTokenTransactionHandler{
		config:           config,
		enablePreemption: false,
		prepareHandler:   NewInternalPrepareTokenHandler(config),
	}
}

// NewStartTokenTransactionHandlerWithPreemption creates a new StartTokenTransactionHandler with pre-emption enabled.
func NewStartTokenTransactionHandlerWithPreemption(config *so.Config) *StartTokenTransactionHandler {
	return &StartTokenTransactionHandler{
		config:           config,
		enablePreemption: true,
		prepareHandler:   NewInternalPrepareTokenHandlerWithPreemption(config),
	}
}

// StartTokenTransaction verifies the token outputs, verifies any attached spark invoices, reserves the keyshares for the token transaction, and returns metadata about the operators that possess the keyshares.
func (h *StartTokenTransactionHandler) StartTokenTransaction(ctx context.Context, req *tokenpb.StartTransactionRequest) (*tokenpb.StartTransactionResponse, error) {
	ctx, span := tracer.Start(ctx, "StartTokenTransactionHandler.StartTokenTransaction", getTokenTransactionAttributes(req.PartialTokenTransaction))
	defer span.End()

	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, idPubKey); err != nil {
		return nil, tokens.FormatErrorWithTransactionProto(tokens.ErrIdentityPublicKeyAuthFailed, req.PartialTokenTransaction, err)
	}

	if err := utils.ValidatePartialTokenTransaction(req.PartialTokenTransaction, req.PartialTokenTransactionOwnerSignatures, h.config.GetSigningOperatorList(), h.config.SupportedNetworks, h.config.Token.RequireTokenIdentifierForMints, h.config.Token.RequireTokenIdentifierForTransfers); err != nil {
		return nil, tokens.FormatErrorWithTransactionProto(tokens.ErrInvalidPartialTokenTransaction, req.PartialTokenTransaction, err)
	}

	partialTokenTransactionHash, err := utils.HashTokenTransaction(req.PartialTokenTransaction, true)
	ctx, _ = logging.WithAttrs(ctx, tokens.GetPartialTokenTransactionAttrs(partialTokenTransactionHash))
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToHashPartialTransaction, req.PartialTokenTransaction, err)
	}

	previouslyCreatedTokenTransaction, err := ent.FetchPartialTokenTransactionData(ctx, partialTokenTransactionHash)
	if err != nil && !ent.IsNotFound(err) {
		return nil, tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToFetchPartialTransaction, req.PartialTokenTransaction, err)
	}

	// Check that the previous created transaction was found and that it is still in the started state.
	// Also, check that this SO was the coordinator for the transaction. This is necessary because only the coordinator
	// receives direct evidence from each SO individually that a threshold of SOs have validated and saved the transaction.
	if previouslyCreatedTokenTransaction != nil &&
		previouslyCreatedTokenTransaction.Status == st.TokenTransactionStatusStarted {
		coordinatorPubKey, err := keys.ParsePublicKey(previouslyCreatedTokenTransaction.CoordinatorPublicKey)
		if err != nil {
			return nil, err
		}
		if coordinatorPubKey.Equals(h.config.IdentityPublicKey()) {
			ctx, logger := logging.WithAttrs(ctx, tokens.GetEntTokenTransactionAttrs(previouslyCreatedTokenTransaction))
			logger.Info("Found existing token transaction in started state with matching coordinator")
			return h.regenerateStartResponseForDuplicateRequest(ctx, previouslyCreatedTokenTransaction)
		}
	}

	if h.enablePreemption {
		if err := preemptOrRejectTransactions(ctx, req.PartialTokenTransaction); err != nil {
			return nil, err
		}
	}

	if req.PartialTokenTransaction.Version >= 2 && len(req.PartialTokenTransaction.InvoiceAttachments) > 0 {
		if err := validateSparkInvoicesForTransaction(ctx, req.PartialTokenTransaction); err != nil {
			return nil, err
		}
	}

	finalTokenTransaction, keyshareIDStrings, err := h.constructFinalTokenTransaction(ctx, req.PartialTokenTransaction, req.ValidityDurationSeconds)
	if err != nil {
		return nil, err
	}
	finalHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	if err != nil {
		return nil, err
	}
	ctx, _ = logging.WithAttrs(ctx, tokens.GetFinalizedTokenTransactionAttrs(finalHash))

	// Save the token transaction object to lock in the revocation commitments for each created output within this transaction.
	// Note that atomicity here is very important to ensure that the unused keyshares queried above are not used by another operation.
	// This property should be help because the coordinator blocks on the other SO responses.
	allExceptSelfSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &allExceptSelfSelection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		return nil, callPrepareTokenTransactionInternal(ctx,
			operator,
			finalTokenTransaction,
			req.PartialTokenTransactionOwnerSignatures,
			keyshareIDStrings,
			h.config.IdentityPublicKey(),
			// If pre-emption is enabled, we need to call spark_token_internal.PrepareTransaction;
			// otherwise we call spark_internal.StartTokenTransactionInternal.
			h.enablePreemption,
		)
	})
	if err != nil {
		formattedError := tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToExecuteWithNonCoordinator, req.PartialTokenTransaction, err)
		grpcStatus, ok := status.FromError(err)
		if ok {
			formattedError = errors.WrapErrorWithGRPCCode(formattedError, grpcStatus.Code())
		}
		return nil, formattedError
	}

	// Only save in the coordinator SO after receiving confirmation from all other SOs. This ensures that if
	// a follow-up call is made that the coordinator has only saved the data if the initial Start call reached the SO threshold.
	// For self-calls, use the handler directly to avoid deadlocks
	_, err = h.prepareHandler.PrepareTokenTransactionInternal(ctx, &tokeninternalpb.PrepareTransactionRequest{
		KeyshareIds:                keyshareIDStrings,
		FinalTokenTransaction:      finalTokenTransaction,
		TokenTransactionSignatures: req.PartialTokenTransactionOwnerSignatures,
		CoordinatorPublicKey:       h.config.IdentityPublicKey().Serialize(),
	})
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToExecuteWithCoordinator, req.PartialTokenTransaction, err)
	}

	keyshareInfo, err := getStartTokenTransactionKeyshareInfo(h.config)
	if keyshareInfo == nil {
		return nil, tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToGetKeyshareInfo, req.PartialTokenTransaction, err)
	}

	return &tokenpb.StartTransactionResponse{
		FinalTokenTransaction: finalTokenTransaction,
		KeyshareInfo:          keyshareInfo,
	}, nil
}

// callPrepareTokenTransactionInternal handles calling the PrepareTokenTransactionInternal RPC on an operator
func callPrepareTokenTransactionInternal(ctx context.Context, operator *so.SigningOperator,
	finalTokenTransaction *tokenpb.TokenTransaction, signaturesWithIndex []*tokenpb.SignatureWithIndex,
	keyshareIDStrings []string, coordinatorPublicKey keys.Public,
	callSparkTokenInternal bool,
) error {
	ctx, span := tracer.Start(ctx, "StartTokenTransactionHandler.callPrepareTokenTransactionInternal", getTokenTransactionAttributes(finalTokenTransaction))
	defer span.End()
	conn, err := operator.NewOperatorGRPCConnection()
	if err != nil {
		return tokens.FormatErrorWithTransactionProto(fmt.Sprintf(tokens.ErrFailedToConnectToOperator, operator.Identifier), finalTokenTransaction, err)
	}
	defer conn.Close()

	tokenReq := &tokeninternalpb.PrepareTransactionRequest{
		KeyshareIds:                keyshareIDStrings,
		FinalTokenTransaction:      finalTokenTransaction,
		TokenTransactionSignatures: signaturesWithIndex,
		CoordinatorPublicKey:       coordinatorPublicKey.Serialize(),
	}
	if callSparkTokenInternal {
		client := tokeninternalpb.NewSparkTokenInternalServiceClient(conn)
		_, err = client.PrepareTransaction(ctx, tokenReq)
	} else {
		client := sparkinternalpb.NewSparkInternalServiceClient(conn)
		var sparkReq *sparkinternalpb.StartTokenTransactionInternalRequest
		sparkReq, err = protoconverter.SparkStartTokenTransactionInternalRequestFromTokenProto(tokenReq)
		if err != nil {
			return fmt.Errorf("%s: %w", fmt.Sprintf(tokens.ErrFailedToConvertTokenProto, "PrepareTransactionRequest", "StartTokenTransactionInternalRequest"), err)
		}
		_, err = client.StartTokenTransactionInternal(ctx, sparkReq)
	}
	if err != nil {
		return tokens.FormatErrorWithTransactionProto(fmt.Sprintf(tokens.ErrFailedToExecuteWithOperator, operator.Identifier), finalTokenTransaction, err)
	}
	return err
}

func getStartTokenTransactionKeyshareInfo(config *so.Config) (*sparkpb.SigningKeyshare, error) {
	allOperators := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	operatorList, err := allOperators.OperatorList(config)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToGetOperatorList, err)
	}
	operatorIdentifiers := make([]string, len(operatorList))
	for i, operator := range operatorList {
		operatorIdentifiers[i] = operator.Identifier
	}
	return &sparkpb.SigningKeyshare{
		OwnerIdentifiers: operatorIdentifiers,
		// TODO: Unify threshold type (uint32 vs uint64) at all callsites between protos and config.
		Threshold: uint32(config.Threshold),
	}, nil
}

// regenerateStartResponseForDuplicateRequest handles the case where a Start() recall has been received for a
// partial token transaction which has already been started. This allows for simpler wallet SDK logic such that
// if a later SignTokenTransaction() call to one of the SOs failed - the wallet SDK can retry from the beginning
// and retrieve the original final token transaction which was started before signing among all parties.
// This does not allow for retrying a Start call that was incomplete due to a downstream error.  A repeat
// request for the same transaction that was not fully started will generate a fresh final token transaction
// with different revocation keys.
func (h *StartTokenTransactionHandler) regenerateStartResponseForDuplicateRequest(
	ctx context.Context,
	tokenTransaction *ent.TokenTransaction,
) (*tokenpb.StartTransactionResponse, error) {
	_, logger := logging.WithAttrs(ctx, tokens.GetEntTokenTransactionAttrs(tokenTransaction))
	logger.Debug("Regenerating response for a duplicate StartTokenTransaction() Call")
	var invalidOutputs []string
	expectedCreatedOutputStatus := st.TokenOutputStatusCreatedStarted

	invalidOutputs = validateOutputs(tokenTransaction.Edges.CreatedOutput, expectedCreatedOutputStatus)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputs(tokenTransaction.Edges.SpentOutput, st.TokenOutputStatusSpentStarted)...)
	}
	if len(invalidOutputs) > 0 {
		return nil, tokens.FormatErrorWithTransactionEnt(
			fmt.Sprintf("%s: %s",
				tokens.ErrInvalidOutputs,
				strings.Join(invalidOutputs, "; ")),
			tokenTransaction, nil)
	}

	// Reconstruct the token transaction from the ent data.
	transaction, err := tokenTransaction.MarshalProto(h.config)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToMarshalTokenTransaction, tokenTransaction, err)
	}

	keyshareInfo, err := getStartTokenTransactionKeyshareInfo(h.config)
	if keyshareInfo == nil {
		return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToGetKeyshareInfo, tokenTransaction, err)
	}

	logger.Debug("Returning stored final token transaction in response to repeat start call")
	return &tokenpb.StartTransactionResponse{
		FinalTokenTransaction: transaction,
		KeyshareInfo:          keyshareInfo,
	}, nil
}

// preemptOrRejectTransactions handles the pre-emption logic during StartTokenTransaction
// It checks if the transaction is being pre-empted by/is pre-empting an existing transaction and cancels the existing transaction if necessary.
func preemptOrRejectTransactions(
	ctx context.Context,
	tokenTransaction *tokenpb.TokenTransaction,
) error {
	tokenTransactionType, err := utils.InferTokenTransactionType(tokenTransaction)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("Cannot determine token transaction type", tokenTransaction, err)
	}

	if tokenTransactionType == utils.TokenTransactionTypeTransfer {
		outputsToSpend := tokenTransaction.GetTransferInput().GetOutputsToSpend() // TODO: This is a hack to get the outputs to spend.

		// Fetch input TTXOs and check if any are already being spent
		inputTtxos, err := ent.FetchAndLockTokenInputs(ctx, outputsToSpend)
		if err != nil {
			return tokens.FormatErrorWithTransactionProto("failed to fetch input TTXOs", tokenTransaction, err)
		}

		if err := preemptOrRejectTransactionsWithInputEnts(ctx, tokenTransaction, inputTtxos); err != nil {
			return err
		}
	}

	// For mint transactions, there are no input outputs to check, so no pre-emption needed
	return nil
}

// preemptOrRejectTransactionsWithInputEnts is an optimized pre-emption check that uses pre-loaded input TTXOs
// to avoid additional database queries
func preemptOrRejectTransactionsWithInputEnts(
	ctx context.Context,
	tokenTransaction *tokenpb.TokenTransaction,
	inputTtxos []*ent.TokenOutput,
) error {
	competingTransactionIDs := make(map[uuid.UUID]*ent.TokenTransaction)
	for _, ttxo := range inputTtxos {
		if ttxo.Edges.OutputSpentTokenTransaction != nil {
			competingTx := ttxo.Edges.OutputSpentTokenTransaction

			if competingTx.Status == st.TokenTransactionStatusStartedCancelled ||
				competingTx.Status == st.TokenTransactionStatusSignedCancelled {
				continue
			}

			// Skip expired transactions (they automatically lose)
			if !competingTx.ExpiryTime.IsZero() && competingTx.ExpiryTime.Before(time.Now()) {
				continue
			}

			competingTransactionIDs[competingTx.ID] = competingTx
		}
	}

	for _, competingTx := range competingTransactionIDs {
		if err := preemptOrRejectTransaction(ctx, tokenTransaction, competingTx); err != nil {
			return err
		}
	}

	return nil
}

// preemptOrRejectTransaction implements the racing logic for token transactions.
// It compares client timestamps first (earlier timestamp wins), then falls back to partial hash comparison.
// Returns an error if the new transaction should be rejected, nil if it should proceed.
func preemptOrRejectTransaction(
	ctx context.Context,
	newTransaction *tokenpb.TokenTransaction,
	existingTransaction *ent.TokenTransaction,
) error {
	// First, compare client timestamps if both transactions have them
	if newTransaction.ClientCreatedTimestamp != nil && !existingTransaction.ClientCreatedTimestamp.IsZero() {
		newTimestamp := newTransaction.ClientCreatedTimestamp.AsTime()
		existingTimestamp := existingTransaction.ClientCreatedTimestamp

		if newTimestamp.Before(existingTimestamp) {
			return logWillPreemptExistingTransaction(ctx, existingTransaction, "earlier timestamp", fmt.Sprintf("new=%s, existing=%s", newTimestamp.Format(time.RFC3339Nano), existingTimestamp.Format(time.RFC3339Nano)))
		} else if newTimestamp.After(existingTimestamp) {
			return rejectNewTransaction(ctx, newTransaction, existingTransaction, "earlier timestamp",
				fmt.Sprintf("new=%s, existing=%s", newTimestamp.Format(time.RFC3339Nano), existingTimestamp.Format(time.RFC3339Nano)))
		}
	}

	// Fall back to hash comparison if timestamps are equal or not available
	existingPartialHash := existingTransaction.PartialTokenTransactionHash
	newPartialHash, err := utils.HashTokenTransaction(newTransaction, true)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToHashPartialTransaction, newTransaction, err)
	}

	if bytes.Compare(newPartialHash, existingPartialHash) < 0 {
		return logWillPreemptExistingTransaction(ctx, existingTransaction, "better partial hash", fmt.Sprintf("new=%x, existing=%x", newPartialHash, existingPartialHash))
	} else {
		return rejectNewTransaction(ctx, newTransaction, existingTransaction, "better partial hash",
			fmt.Sprintf("new=%x, existing=%x", newPartialHash, existingPartialHash))
	}
}

// logWillPreemptExistingTransaction logs that we will pre-empt the existing transaction
func logWillPreemptExistingTransaction(ctx context.Context, existingTransaction *ent.TokenTransaction, reason, details string) error {
	_, logger := logging.WithAttrs(ctx, getPreviousEntTokenTransactionAttrs(existingTransaction))
	logger.Info(fmt.Sprintf("Pre-empting existing transaction with new transaction (%s: %s)", reason, details))
	return nil
}

// rejectNewTransaction rejects the new transaction and logs the action
func rejectNewTransaction(ctx context.Context, newTransaction *tokenpb.TokenTransaction, existingTransaction *ent.TokenTransaction, reason, details string) error {
	_, logger := logging.WithAttrs(ctx, getPreviousEntTokenTransactionAttrs(existingTransaction))
	logger.Info(fmt.Sprintf("Rejecting new transaction due to existing transaction having %s (%s)", reason, details))
	return tokens.NewTransactionPreemptedError(newTransaction, reason, details)
}

func getPreviousEntTokenTransactionAttrs(tokenTransaction *ent.TokenTransaction) []logging.Attr {
	return []logging.Attr{
		{Key: "previous_transaction_uuid", Value: tokenTransaction.ID.String()},
		{Key: "previous_transaction_hash", Value: hex.EncodeToString(tokenTransaction.FinalizedTokenTransactionHash)},
	}
}

// constructFinalTokenTransaction constructs the final token transaction from the partial token transaction
// by setting expiry time, allocating keyshares, and filling output details.
func (h *StartTokenTransactionHandler) constructFinalTokenTransaction(
	ctx context.Context,
	partialTokenTransaction *tokenpb.TokenTransaction,
	validityDurationSeconds uint64,
) (*tokenpb.TokenTransaction, []string, error) {
	finalTokenTransaction := partialTokenTransaction
	if validityDurationSeconds > spark.TokenMaxValidityDuration {
		return nil, nil, tokens.FormatErrorWithTransactionProto(tokens.ErrInvalidValidityDuration, partialTokenTransaction,
			fmt.Errorf("validity duration seconds too large: %d, maximum value: %d", validityDurationSeconds, spark.TokenMaxValidityDuration))
	}
	validityDuration := time.Duration(validityDurationSeconds) * time.Second
	finalTokenTransaction.ExpiryTime = timestamppb.New(time.Now().Add(validityDuration))

	if partialTokenTransaction.InvoiceAttachments != nil {
		now := time.Now().UTC()
		minInvoiceExpiration := time.Time{}
		for _, attachment := range partialTokenTransaction.InvoiceAttachments {
			invoice := attachment.GetSparkInvoice()
			parsedInvoice, err := common.ParseSparkInvoice(invoice)
			if err != nil {
				return nil, nil, tokens.FormatErrorWithTransactionProtoAndSparkInvoice("failed to parse spark invoice", partialTokenTransaction, invoice, err)
			}
			if parsedInvoice.ExpiryTime != nil {
				expiryTime := parsedInvoice.ExpiryTime.AsTime().UTC()
				if expiryTime.Before(now) {
					return nil, nil, tokens.FormatErrorWithTransactionProtoAndSparkInvoice(tokens.ErrSparkInvoiceExpired, partialTokenTransaction, invoice, fmt.Errorf("spark invoice is expired: %s", expiryTime.Format(time.RFC3339Nano)))
				}
				if minInvoiceExpiration.IsZero() || expiryTime.Before(minInvoiceExpiration) {
					minInvoiceExpiration = expiryTime
				}
			}
		}
		if !minInvoiceExpiration.IsZero() && minInvoiceExpiration.Before(finalTokenTransaction.ExpiryTime.AsTime()) {
			finalTokenTransaction.ExpiryTime = timestamppb.New(minInvoiceExpiration)
		}
	}

	// Determine transaction type and handle type-specific logic
	txType, err := utils.InferTokenTransactionType(finalTokenTransaction)
	if err != nil {
		return nil, nil, tokens.FormatErrorWithTransactionProto("failed to infer token transaction type", partialTokenTransaction, err)
	}

	numOutputs := len(partialTokenTransaction.TokenOutputs)
	keyshareIDStrings := make([]string, numOutputs)

	switch txType {
	case utils.TokenTransactionTypeCreate:
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}
		createInput := finalTokenTransaction.GetCreateInput()
		creationEntityPublicKey, err := ent.GetEntityDkgKeyPublicKey(ctx, db.Client())
		if err != nil {
			return nil, nil, tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToGetCreationEntityPublicKey, partialTokenTransaction, err)
		}
		createInput.CreationEntityPublicKey = creationEntityPublicKey.Serialize()
	case utils.TokenTransactionTypeMint, utils.TokenTransactionTypeTransfer:
		// Mint and transfer transactions create outputs that require keyshares for revocation commitments
		if numOutputs > 0 {
			keyshares, err := ent.GetUnusedSigningKeyshares(ctx, h.config, numOutputs)
			if err != nil {
				return nil, nil, tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToGetUnusedKeyshares, partialTokenTransaction, err)
			}

			if len(keyshares) < numOutputs {
				return nil, nil, tokens.FormatErrorWithTransactionProto(
					tokens.ErrFailedToGetUnusedKeyshares, partialTokenTransaction,
					fmt.Errorf("%s: %d needed, %d available", tokens.ErrNotEnoughUnusedKeyshares, numOutputs, len(keyshares)))
			}

			keyshareIDs := make([]uuid.UUID, len(keyshares))
			for i, keyshare := range keyshares {
				keyshareIDs[i] = keyshare.ID
				keyshareIDStrings[i] = keyshare.ID.String()
			}
			network, err := common.NetworkFromProtoNetwork(partialTokenTransaction.Network)
			if err != nil {
				return nil, nil, tokens.FormatErrorWithTransactionProto(tokens.ErrFailedToGetNetworkFromProto, partialTokenTransaction, err)
			}

			// Fill revocation commitments and withdrawal bond/locktime for each output.
			for i, output := range finalTokenTransaction.TokenOutputs {
				id, err := uuid.NewV7()
				if err != nil {
					return nil, nil, err
				}
				idStr := id.String()
				output.Id = &idStr
				output.RevocationCommitment = keyshares[i].PublicKey
				withdrawalBondSats := h.config.Lrc20Configs[network.String()].WithdrawBondSats
				output.WithdrawBondSats = &withdrawalBondSats
				withdrawRelativeBlockLocktime := h.config.Lrc20Configs[network.String()].WithdrawRelativeBlockLocktime
				output.WithdrawRelativeBlockLocktime = &withdrawRelativeBlockLocktime
			}
		}

	default:
		return nil, nil, tokens.FormatErrorWithTransactionProto("unknown token transaction type", partialTokenTransaction,
			fmt.Errorf("unsupported transaction type: %s", txType.String()))
	}

	return finalTokenTransaction, keyshareIDStrings, nil
}
