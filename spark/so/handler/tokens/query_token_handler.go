package tokens

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/errors"

	"github.com/lightsparkdev/spark/common"

	"github.com/lightsparkdev/spark/so/protoconverter"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/tokens"
)

const (
	DefaultTokenOutputPageSize = 500
	MaxTokenOutputPageSize     = 500
)

type QueryTokenHandler struct {
	config                     *so.Config
	includeExpiredTransactions bool
}

// NewQueryTokenHandler creates a new QueryTokenHandler.
func NewQueryTokenHandler(config *so.Config) *QueryTokenHandler {
	return &QueryTokenHandler{
		config:                     config,
		includeExpiredTransactions: false,
	}
}

func NewQueryTokenHandlerWithExpiredTransactions(config *so.Config) *QueryTokenHandler {
	return &QueryTokenHandler{
		config:                     config,
		includeExpiredTransactions: true,
	}
}

func (h *QueryTokenHandler) QueryTokenMetadata(ctx context.Context, req *tokenpb.QueryTokenMetadataRequest) (*tokenpb.QueryTokenMetadataResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.QueryTokenMetadata")
	defer span.End()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	if len(req.TokenIdentifiers) == 0 && len(req.IssuerPublicKeys) == 0 {
		return nil, fmt.Errorf("must provide at least one token identifier or issuer public key")
	}

	fields := []string{
		tokencreate.FieldIssuerPublicKey,
		tokencreate.FieldTokenName,
		tokencreate.FieldTokenTicker,
		tokencreate.FieldDecimals,
		tokencreate.FieldMaxSupply,
		tokencreate.FieldIsFreezable,
		tokencreate.FieldCreationEntityPublicKey,
		tokencreate.FieldNetwork,
	}

	query := db.TokenCreate.Query()
	var conditions []predicate.TokenCreate
	if len(req.TokenIdentifiers) > 0 {
		conditions = append(conditions, tokencreate.TokenIdentifierIn(req.TokenIdentifiers...))
	}

	if len(req.IssuerPublicKeys) > 0 {
		conditions = append(conditions, tokencreate.IssuerPublicKeyIn(req.IssuerPublicKeys...))
	}
	query = query.Where(tokencreate.Or(conditions...))
	tokenCreateEntities, err := query.Select(fields...).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query token metadata: %w", err)
	}

	var tokenMetadataList []*tokenpb.TokenMetadata
	for _, tokenCreate := range tokenCreateEntities {
		tokenMetadata, err := tokenCreate.ToTokenMetadata()
		if err != nil {
			return nil, fmt.Errorf("failed to convert token create to token metadata: %w", err)
		}
		tokenMetadataList = append(tokenMetadataList, tokenMetadata.ToTokenMetadataProto())
	}

	return &tokenpb.QueryTokenMetadataResponse{
		TokenMetadata: tokenMetadataList,
	}, nil
}

// QueryTokenTransactions returns SO provided data about specific token transactions along with their status.
// Allows caller to specify data to be returned related to:
// a) transactions associated with a particular set of output ids
// b) transactions associated with a particular set of transaction hashes
// c) all transactions associated with a particular token public key
func (h *QueryTokenHandler) QueryTokenTransactions(ctx context.Context, req *sparkpb.QueryTokenTransactionsRequest) (*sparkpb.QueryTokenTransactionsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.QueryTokenTransactions")
	defer span.End()
	// Convert sparkpb request to tokenpb request
	tokenReq := protoconverter.TokenProtoQueryTokenTransactionsRequestFromSpark(req)

	// Call internal method with tokenpb
	tokenResp, err := h.queryTokenTransactionsInternal(ctx, tokenReq)
	if err != nil {
		return nil, err
	}

	// Convert tokenpb response back to sparkpb response
	return protoconverter.SparkQueryTokenTransactionsResponseFromTokenProto(tokenResp)
}

// queryTokenTransactionsInternal is the internal implementation using tokenpb protos
func (h *QueryTokenHandler) queryTokenTransactionsInternal(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.queryTokenTransactionsInternal")
	defer span.End()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Start with a base query for token transactions
	baseQuery := db.TokenTransaction.Query()

	// Apply filters based on request parameters
	if len(req.OutputIds) > 0 {
		// Convert string IDs to UUIDs
		outputUUIDs := make([]uuid.UUID, 0, len(req.OutputIds))
		for _, idStr := range req.OutputIds {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return nil, fmt.Errorf("invalid output ID format: %w", err)
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

	if len(req.OwnerPublicKeys) > 0 {
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.OwnerPublicKeyIn(req.OwnerPublicKeys...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.OwnerPublicKeyIn(req.OwnerPublicKeys...)),
			),
		)
	}

	if len(req.IssuerPublicKeys) > 0 {
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.TokenPublicKeyIn(req.IssuerPublicKeys...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.TokenPublicKeyIn(req.IssuerPublicKeys...)),
			),
		)
	}

	if len(req.TokenIdentifiers) > 0 {
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.TokenIdentifierIn(req.TokenIdentifiers...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.TokenIdentifierIn(req.TokenIdentifiers...)),
			),
		)
	}

	// Apply sorting, limit and offset
	query := baseQuery.Order(ent.Desc(tokentransaction.FieldUpdateTime))

	if req.Limit == 0 {
		req.Limit = 100
	}

	if req.Limit > 1000 {
		req.Limit = 1000
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
		}).
		WithMint().
		WithSparkInvoice()

	// Execute the query
	transactions, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query token transactions: %w", err)
	}

	// Convert to response protos
	transactionsWithStatus := make([]*tokenpb.TokenTransactionWithStatus, 0, len(transactions))
	for _, transaction := range transactions {
		// Determine transaction status based on output statuses.
		status := protoconverter.ConvertTokenTransactionStatusToTokenPb(transaction.Status)

		// Reconstruct the token transaction from the ent data.
		transactionProto, err := transaction.MarshalProto(h.config)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToMarshalTokenTransaction, transaction, err)
		}

		transactionWithStatus := &tokenpb.TokenTransactionWithStatus{
			TokenTransaction:     transactionProto,
			Status:               status,
			TokenTransactionHash: transaction.FinalizedTokenTransactionHash,
		}

		if status == tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_FINALIZED {
			spentTokenOutputsMetadata := make([]*tokenpb.SpentTokenOutputMetadata, 0, len(transaction.Edges.SpentOutput))

			for _, spentOutput := range transaction.Edges.SpentOutput {
				spentTokenOutputsMetadata = append(spentTokenOutputsMetadata, &tokenpb.SpentTokenOutputMetadata{
					OutputId:         spentOutput.ID.String(),
					RevocationSecret: spentOutput.SpentRevocationSecret,
				})
			}
			transactionWithStatus.ConfirmationMetadata = &tokenpb.TokenTransactionConfirmationMetadata{
				SpentTokenOutputsMetadata: spentTokenOutputsMetadata,
			}
		}
		transactionsWithStatus = append(transactionsWithStatus, transactionWithStatus)
	}

	// Calculate next offset
	var nextOffset int64
	if len(transactions) == int(req.Limit) {
		nextOffset = req.Offset + int64(len(transactions))
	} else {
		nextOffset = -1
	}

	return &tokenpb.QueryTokenTransactionsResponse{
		TokenTransactionsWithStatus: transactionsWithStatus,
		Offset:                      nextOffset,
	}, nil
}

// QueryTokenTransactionsToken is the native tokenpb endpoint for SparkTokenService.
// This provides the same functionality as the legacy QueryTokenTransactions but uses
// tokenpb protocol directly for better performance and cleaner API design.
func (h *QueryTokenHandler) QueryTokenTransactionsToken(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	// Directly use the internal implementation since it already uses tokenpb natively
	return h.queryTokenTransactionsInternal(ctx, req)
}

func (h *QueryTokenHandler) QueryTokenOutputs(
	ctx context.Context,
	req *sparkpb.QueryTokenOutputsRequest,
) (*sparkpb.QueryTokenOutputsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.QueryTokenOutputs")
	defer span.End()
	// Convert sparkpb request to tokenpb request
	tokenReq := protoconverter.TokenProtoQueryTokenOutputsRequestFromSpark(req)

	// Call internal method with tokenpb
	tokenResp, err := h.queryTokenOutputsInternal(ctx, tokenReq)
	if err != nil {
		return nil, err
	}

	// Convert tokenpb response back to sparkpb response
	return protoconverter.SparkQueryTokenOutputsResponseFromTokenProto(tokenResp), nil
}

// queryTokenOutputsInternal is the internal implementation using tokenpb protos
func (h *QueryTokenHandler) queryTokenOutputsInternal(
	ctx context.Context,
	req *tokenpb.QueryTokenOutputsRequest,
) (*tokenpb.QueryTokenOutputsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.queryTokenOutputsInternal")
	defer span.End()
	logger := logging.GetLoggerFromContext(ctx)

	// Convert tokenpb request to sparkpb request for internal service calls
	// This is necessary because the internal services still use sparkpb
	sparkReq := &sparkpb.QueryTokenOutputsRequest{
		OwnerPublicKeys:  req.OwnerPublicKeys,
		TokenPublicKeys:  req.IssuerPublicKeys, // Field name change: IssuerPublicKeys -> TokenPublicKeys
		TokenIdentifiers: req.TokenIdentifiers,
		Network:          req.Network,
	}

	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	responses, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &allSelection,
		func(ctx context.Context, operator *so.SigningOperator) (map[string]*sparkpb.OutputWithPreviousTransactionData, error) {
			var availableOutputs *sparkpb.QueryTokenOutputsResponse
			var err error

			if operator.Identifier == h.config.Identifier {
				availableOutputs, err = h.QueryTokenOutputsSpark(ctx, sparkReq)
				if err != nil {
					return nil, fmt.Errorf("failed to query token outputs from operator %s: %w", operator.Identifier, err)
				}
			} else {
				conn, err := operator.NewOperatorGRPCConnection()
				if err != nil {
					return nil, fmt.Errorf("failed to connect to operator %s: %w", operator.Identifier, err)
				}
				defer conn.Close()

				client := pbinternal.NewSparkInternalServiceClient(conn)
				availableOutputs, err = client.QueryTokenOutputsInternal(ctx, sparkReq)
				if err != nil {
					return nil, fmt.Errorf("failed to query token outputs from operator %s: %w", operator.Identifier, err)
				}
			}

			spendableOutputMap := make(map[string]*sparkpb.OutputWithPreviousTransactionData)
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
	spendableOutputs := make([]*sparkpb.OutputWithPreviousTransactionData, 0)
	countSpendableOperatorsForOutputID := make(map[string]int)

	requiredSpendableOperators := len(h.config.GetSigningOperatorList())
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

	// Convert sparkpb response to tokenpb response
	tokenOutputs := make([]*tokenpb.OutputWithPreviousTransactionData, len(spendableOutputs))
	for i, sparkOutput := range spendableOutputs {
		tokenOutputs[i] = &tokenpb.OutputWithPreviousTransactionData{
			Output: &tokenpb.TokenOutput{
				Id:                            sparkOutput.Output.Id,
				OwnerPublicKey:                sparkOutput.Output.OwnerPublicKey,
				RevocationCommitment:          sparkOutput.Output.RevocationCommitment,
				WithdrawBondSats:              sparkOutput.Output.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: sparkOutput.Output.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                sparkOutput.Output.TokenPublicKey,
				TokenAmount:                   sparkOutput.Output.TokenAmount,
				TokenIdentifier:               sparkOutput.Output.TokenIdentifier,
			},
			PreviousTransactionHash: sparkOutput.PreviousTransactionHash,
			PreviousTransactionVout: sparkOutput.PreviousTransactionVout,
		}
	}

	return &tokenpb.QueryTokenOutputsResponse{
		OutputsWithPreviousTransactionData: tokenOutputs,
	}, nil
}

func (h *QueryTokenHandler) QueryTokenOutputsSpark(ctx context.Context, req *sparkpb.QueryTokenOutputsRequest) (*sparkpb.QueryTokenOutputsResponse, error) {
	tokenReq := protoconverter.TokenProtoQueryTokenOutputsRequestFromSpark(req)

	tokenResp, err := h.QueryTokenOutputsToken(ctx, tokenReq)
	if err != nil {
		return nil, err
	}

	return protoconverter.SparkQueryTokenOutputsResponseFromTokenProto(tokenResp), nil
}

// QueryTokenOutputsToken is the native tokenpb endpoint for SparkTokenService.
// This provides the same functionality as the legacy QueryTokenOutputs but uses
// tokenpb protocol directly for better performance and cleaner API design.
func (h *QueryTokenHandler) QueryTokenOutputsToken(ctx context.Context, req *tokenpb.QueryTokenOutputsRequest) (*tokenpb.QueryTokenOutputsResponse, error) {
	network, err := common.DetermineNetwork(req.GetNetwork())
	if err != nil {
		return nil, err
	}

	ownerPubKeys, err := parsePubKeys(req.GetOwnerPublicKeys())
	if err != nil {
		return nil, errors.InvalidUserInputErrorf("invalid owner public keys: %w", err)
	}
	issuerPubKeys, err := parsePubKeys(req.GetIssuerPublicKeys())
	if err != nil {
		return nil, errors.InvalidUserInputErrorf("invalid issuer public keys: %w", err)
	}
	tokenIdentifiers := req.GetTokenIdentifiers()
	if len(ownerPubKeys) == 0 && len(issuerPubKeys) == 0 && len(tokenIdentifiers) == 0 {
		return nil, errors.InvalidUserInputErrorf("must specify owner public key, issuer public key, or token identifier")
	}

	var afterID *uuid.UUID
	var beforeID *uuid.UUID

	pageRequest := req.GetPageRequest()
	var direction sparkpb.Direction
	var cursor string

	if pageRequest != nil {
		direction = pageRequest.GetDirection()
		cursor = pageRequest.GetCursor()
	}

	// Handle cursor based on direction
	if cursor != "" {
		cursorBytes, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			cursorBytes, err = base64.URLEncoding.DecodeString(cursor)
			if err != nil {
				return nil, errors.InvalidUserInputErrorf("invalid cursor: %v", err)
			}
		}
		id, err := uuid.FromBytes(cursorBytes)
		if err != nil {
			return nil, errors.InvalidUserInputErrorf("invalid cursor: %v", err)
		}

		if direction == sparkpb.Direction_PREVIOUS {
			beforeID = &id
		} else {
			afterID = &id
		}
	}

	limit := DefaultTokenOutputPageSize
	if pageRequest != nil && pageRequest.GetPageSize() > 0 {
		limit = int(pageRequest.GetPageSize())
	}
	if limit > MaxTokenOutputPageSize {
		limit = MaxTokenOutputPageSize
	}

	// Check for unsupported backward pagination
	if direction == sparkpb.Direction_PREVIOUS {
		return nil, errors.InvalidUserInputErrorf("backward pagination with 'previous' direction is not currently supported")
	}

	queryLimit := limit + 1
	outputs, err := ent.GetOwnedTokenOutputs(ctx, ent.GetOwnedTokenOutputsParams{
		OwnerPublicKeys:            ownerPubKeys,
		IssuerPublicKeys:           issuerPubKeys,
		TokenIdentifiers:           tokenIdentifiers,
		IncludeExpiredTransactions: true,
		Network:                    *network,
		AfterID:                    afterID,
		BeforeID:                   beforeID,
		Limit:                      queryLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToGetOwnedOutputStats, err)
	}
	var ownedTokenOutputs []*tokenpb.OutputWithPreviousTransactionData
	for i, output := range outputs {
		if i >= limit {
			break
		}
		idStr := output.ID.String()
		ownedTokenOutputs = append(ownedTokenOutputs, &tokenpb.OutputWithPreviousTransactionData{
			Output: &tokenpb.TokenOutput{
				Id:                            &idStr,
				OwnerPublicKey:                output.OwnerPublicKey,
				RevocationCommitment:          output.WithdrawRevocationCommitment,
				WithdrawBondSats:              &output.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: &output.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                output.TokenPublicKey,
				TokenIdentifier:               output.TokenIdentifier,
				TokenAmount:                   output.TokenAmount,
			},
			PreviousTransactionHash: output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
			PreviousTransactionVout: uint32(output.CreatedTransactionOutputVout),
		})
	}
	pageResponse := &sparkpb.PageResponse{}

	hasMoreResults := len(outputs) > limit

	if afterID != nil {
		// Forward pagination: we know there's a previous page, check if there's a next page
		pageResponse.HasPreviousPage = true
		pageResponse.HasNextPage = hasMoreResults
	} else {
		// No pagination: no previous page, check if there's a next page
		pageResponse.HasPreviousPage = false
		pageResponse.HasNextPage = hasMoreResults
	}

	// Set previous cursor (first item's ID) - for going backward from this page
	if len(ownedTokenOutputs) > 0 {
		if first := ownedTokenOutputs[0]; first != nil && first.Output != nil && first.Output.Id != nil {
			if firstUUID, err := uuid.Parse(*first.Output.Id); err == nil {
				pageResponse.PreviousCursor = base64.RawURLEncoding.EncodeToString(firstUUID[:])
			}
		}
	}

	// Set next cursor (last item's ID) - for going forward from this page
	if len(ownedTokenOutputs) > 0 {
		if last := ownedTokenOutputs[len(ownedTokenOutputs)-1]; last != nil && last.Output != nil && last.Output.Id != nil {
			if lastUUID, err := uuid.Parse(*last.Output.Id); err == nil {
				pageResponse.NextCursor = base64.RawURLEncoding.EncodeToString(lastUUID[:])
			}
		}
	}

	return &tokenpb.QueryTokenOutputsResponse{
		OutputsWithPreviousTransactionData: ownedTokenOutputs,
		PageResponse:                       pageResponse,
	}, nil
}

func parsePubKeys(rawKeys [][]byte) ([]keys.Public, error) {
	parsed := make([]keys.Public, len(rawKeys))
	for i, rawKey := range rawKeys {
		pubKey, err := keys.ParsePublicKey(rawKey)
		if err != nil {
			return nil, err
		}
		parsed[i] = pubKey
	}
	return parsed, nil
}
