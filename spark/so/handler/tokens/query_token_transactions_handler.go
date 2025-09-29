package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/so/protoconverter"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/tokens"
)

type QueryTokenTransactionsHandler struct {
	config *so.Config
}

// NewQueryTokenHandler creates a new QueryTokenHandler.
func NewQueryTokenTransactionsHandler(config *so.Config) *QueryTokenTransactionsHandler {
	return &QueryTokenTransactionsHandler{
		config: config,
	}
}

func (h *QueryTokenTransactionsHandler) QueryTokenTransactionsSpark(ctx context.Context, req *sparkpb.QueryTokenTransactionsRequest) (*sparkpb.QueryTokenTransactionsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.QueryTokenTransactions")
	defer span.End()
	// Convert sparkpb request to tokenpb request
	tokenReq := protoconverter.TokenProtoQueryTokenTransactionsRequestFromSpark(req)

	// Call internal method with tokenpb
	tokenResp, err := h.QueryTokenTransactions(ctx, tokenReq)
	if err != nil {
		return nil, err
	}

	// Convert tokenpb response back to sparkpb response
	return protoconverter.SparkQueryTokenTransactionsResponseFromTokenProto(tokenResp)
}

func (h *QueryTokenTransactionsHandler) QueryTokenTransactions(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
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
		outputUUIDs, err := common.StringUUIDArrayToUUIDArray(req.OutputIds)
		if err != nil {
			return nil, fmt.Errorf("invalid output ID format: %w", err)
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

	ownerPubKeys, err := keys.ParsePublicKeys(req.GetOwnerPublicKeys())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner public key: %w", err)
	}

	if len(ownerPubKeys) > 0 {
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.OwnerPublicKeyIn(ownerPubKeys...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.OwnerPublicKeyIn(ownerPubKeys...)),
			),
		)
	}

	issuerPubKeys, err := keys.ParsePublicKeys(req.GetIssuerPublicKeys())
	if err != nil {
		return nil, fmt.Errorf("failed to parse issuer public key: %w", err)
	}

	if len(issuerPubKeys) > 0 {
		baseQuery = baseQuery.Where(
			tokentransaction.Or(
				tokentransaction.HasCreatedOutputWith(tokenoutput.TokenPublicKeyIn(issuerPubKeys...)),
				tokentransaction.HasSpentOutputWith(tokenoutput.TokenPublicKeyIn(issuerPubKeys...)),
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

	limit := req.GetLimit()
	if limit == 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}
	query = query.Limit(int(limit))

	if req.Offset > 0 {
		query = query.Offset(int(req.Offset))
	}

	// This join respects the query limitations provided above and should only load the necessary relations.
	query = query.
		WithCreatedOutput().
		WithSpentOutput(func(slq *ent.TokenOutputQuery) {
			slq.WithOutputCreatedTokenTransaction()
		}).
		WithCreate().
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
		transactionProto, err := transaction.MarshalProto(ctx, h.config)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToMarshalTokenTransaction, transaction, err)
		}

		transactionWithStatus := &tokenpb.TokenTransactionWithStatus{
			TokenTransaction:     transactionProto,
			Status:               status,
			TokenTransactionHash: transaction.FinalizedTokenTransactionHash,
		}

		if status == tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_FINALIZED {
			spentTokenOutputsMetadata := make([]*tokenpb.SpentTokenOutputMetadata, len(transaction.Edges.SpentOutput))

			for i, spentOutput := range transaction.Edges.SpentOutput {
				spentTokenOutputsMetadata[i] = &tokenpb.SpentTokenOutputMetadata{
					OutputId:         spentOutput.ID.String(),
					RevocationSecret: spentOutput.SpentRevocationSecret,
				}
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
