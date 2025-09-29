package tokens

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"github.com/lightsparkdev/spark/common"

	"github.com/lightsparkdev/spark/so/protoconverter"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/logging"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/tokens"
)

type QueryTokenTransactionsHandler struct {
	config                     *so.Config
	includeExpiredTransactions bool
}

// NewQueryTokenTransactionsHandler creates a new QueryTokenTransactionsHandler.
func NewQueryTokenTransactionsHandler(config *so.Config) *QueryTokenTransactionsHandler {
	return &QueryTokenTransactionsHandler{
		config:                     config,
		includeExpiredTransactions: false,
	}
}

func (h *QueryTokenTransactionsHandler) QueryTokenTransactionsSpark(ctx context.Context, req *sparkpb.QueryTokenTransactionsRequest) (*sparkpb.QueryTokenTransactionsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenTransactionsHandler.QueryTokenTransactions")
	defer span.End()
	// Convert sparkpb request to tokenpb request
	tokenReq := protoconverter.TokenProtoQueryTokenTransactionsRequestFromSpark(req)

	tokenResp, err := h.QueryTokenTransactions(ctx, tokenReq)
	if err != nil {
		return nil, err
	}

	// Convert tokenpb response back to sparkpb response
	return protoconverter.SparkQueryTokenTransactionsResponseFromTokenProto(tokenResp)
}

// QueryTokenTransactions returns SO provided data about specific token transactions along with their status.
// Allows caller to specify data to be returned related to:
// a) transactions associated with a particular set of output ids
// b) transactions associated with a particular set of transaction hashes
// c) all transactions associated with a particular token public key
func (h *QueryTokenTransactionsHandler) QueryTokenTransactions(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenTransactionsHandler.queryTokenTransactionsInternal")
	defer span.End()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var transactions []*ent.TokenTransaction

	// Check if we should use the optimized UNION query
	useOptimizedQuery := h.shouldUseOptimizedQuery(req)
	if useOptimizedQuery {
		transactions, err = h.queryWithRawSql(ctx, req, db.Client())
		if err != nil {
			return nil, fmt.Errorf("failed to query token transactions with raw sql: %w", err)
		}
	} else {
		transactions, err = h.queryWithEnt(ctx, req, db.Client())
		if err != nil {
			return nil, fmt.Errorf("failed to query token transactions with ent: %w", err)
		}
	}

	return h.convertTransactionsToResponse(ctx, transactions, req)
}

// shouldUseOptimizedQuery determines if we should use the optimized UNION-based query
func (h *QueryTokenTransactionsHandler) shouldUseOptimizedQuery(req *tokenpb.QueryTokenTransactionsRequest) bool {
	// Use optimized query when we have filters that require token_outputs joins
	hasOutputFilters := len(req.OutputIds) > 0 ||
		len(req.GetOwnerPublicKeys()) > 0 ||
		len(req.GetIssuerPublicKeys()) > 0 ||
		len(req.TokenIdentifiers) > 0
	return hasOutputFilters
}

// queryTokenTransactionsRawSql uses raw SQL with UNION for better performance
func (h *QueryTokenTransactionsHandler) queryWithRawSql(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest, db *ent.Client) ([]*ent.TokenTransaction, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenTransactionsHandler.queryTokenTransactionsOptimized")
	defer span.End()

	// Build the optimized UNION query
	query, args, err := h.buildOptimizedQuery(req)
	if err != nil {
		return nil, fmt.Errorf("failed to build optimized query: %w", err)
	}

	// nolint:forbidigo
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute optimized query: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			logging.GetLoggerFromContext(ctx).Error("failed to close rows", zap.Error(cerr))
			span.RecordError(cerr)
		}
	}()

	// Scan the results into a simple struct for ID and update_time
	type transactionResult struct {
		ID         uuid.UUID `json:"id"`
		UpdateTime time.Time `json:"update_time"`
	}

	var results []transactionResult
	for rows.Next() {
		var result transactionResult
		if err := rows.Scan(&result.ID, &result.UpdateTime); err != nil {
			return nil, fmt.Errorf("failed to scan transaction result: %w", err)
		}
		results = append(results, result)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate over rows: %w", err)
	}

	// Extract transaction IDs in the correct order
	var transactions []*ent.TokenTransaction
	if len(results) > 0 {
		transactionIDs := make([]uuid.UUID, len(results))
		for i, result := range results {
			transactionIDs[i] = result.ID
		}

		// Load full transaction data using Ent, preserving order from optimized query
		transactionMap := make(map[uuid.UUID]*ent.TokenTransaction)
		allTransactions, err := db.TokenTransaction.Query().
			Where(tokentransaction.IDIn(transactionIDs...)).
			WithCreatedOutput().
			WithSpentOutput(func(slq *ent.TokenOutputQuery) {
				slq.WithOutputCreatedTokenTransaction()
			}).
			WithCreate().
			WithMint().
			WithSparkInvoice().
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load transaction relations: %w", err)
		}

		for _, tx := range allTransactions {
			transactionMap[tx.ID] = tx
		}

		// Preserve order from optimized query
		transactions = make([]*ent.TokenTransaction, 0, len(results))
		for _, result := range results {
			if tx, exists := transactionMap[result.ID]; exists {
				transactions = append(transactions, tx)
			}
		}
	}

	return transactions, nil
}

// buildOptimizedQuery constructs the raw SQL query with CTEs and UNION approach
func (h *QueryTokenTransactionsHandler) buildOptimizedQuery(req *tokenpb.QueryTokenTransactionsRequest) (string, []any, error) {
	// Initialize query builder
	qb := &queryBuilder{
		args:     make([]any, 0),
		argIndex: 1,
	}

	// Parse owner public keys if provided
	var ownerPubKeys []keys.Public
	if len(req.GetOwnerPublicKeys()) > 0 {
		var err error
		ownerPubKeys, err = keys.ParsePublicKeys(req.GetOwnerPublicKeys())
		if err != nil {
			return "", nil, fmt.Errorf("failed to parse owner public key: %w", err)
		}
	}

	// Parse issuer public keys if provided
	var issuerPubKeys []keys.Public
	if len(req.GetIssuerPublicKeys()) > 0 {
		var err error
		issuerPubKeys, err = keys.ParsePublicKeys(req.GetIssuerPublicKeys())
		if err != nil {
			return "", nil, fmt.Errorf("failed to parse issuer public key: %w", err)
		}
	}

	// Add hash filter parameter if needed
	txHashFilterArgIndex := 0
	if len(req.TokenTransactionHashes) > 0 {
		qb.args = append(qb.args, pq.Array(req.TokenTransactionHashes))
		txHashFilterArgIndex = qb.argIndex
		qb.argIndex++
	}

	qb.txHashFilter = buildTxHashFilter(req, txHashFilterArgIndex)

	// Handle OutputIds filter
	// Example query: WITH output_id_filtered AS (SELECT tou.token_output_output_created_token_transaction, tou.token_output_output_spent_token_transaction FROM token_outputs tou WHERE tou.id = ANY($1))
	//                SELECT tt.id, tt.update_time FROM token_transactions tt JOIN output_id_filtered ON tt.id = output_id_filtered.token_output_output_created_token_transaction
	//                UNION SELECT tt.id, tt.update_time FROM token_transactions tt JOIN output_id_filtered ON tt.id = output_id_filtered.token_output_output_spent_token_transaction
	if len(req.OutputIds) > 0 {
		// Convert string IDs to UUIDs
		outputUUIDs, err := common.StringUUIDArrayToUUIDArray(req.OutputIds)
		if err != nil {
			return "", nil, fmt.Errorf("invalid output ID format: %w", err)
		}
		qb.addFilterCTEAndUnions("output_id_filtered", "tou.id = ANY($%d)", pq.Array(outputUUIDs))
	}

	// Handle OwnerPublicKeys filter
	// Example query: WITH owner_key_filtered AS (SELECT tou.token_output_output_created_token_transaction, tou.token_output_output_spent_token_transaction FROM token_outputs tou WHERE tou.owner_public_key = ANY($1))
	//                SELECT tt.id, tt.update_time FROM token_transactions tt JOIN owner_key_filtered ON tt.id = owner_key_filtered.token_output_output_created_token_transaction
	//                UNION SELECT tt.id, tt.update_time FROM token_transactions tt JOIN owner_key_filtered ON tt.id = owner_key_filtered.token_output_output_spent_token_transaction
	if len(ownerPubKeys) > 0 {
		ownerKeyBytes := make([][]byte, len(ownerPubKeys))
		for i, key := range ownerPubKeys {
			ownerKeyBytes[i] = key.Serialize()
		}
		qb.addFilterCTEAndUnions("owner_key_filtered", "tou.owner_public_key = ANY($%d)", pq.Array(ownerKeyBytes))
	}

	// Handle IssuerPublicKeys filter
	// Example query: WITH issuer_key_filtered AS (SELECT tou.token_output_output_created_token_transaction, tou.token_output_output_spent_token_transaction FROM token_outputs tou WHERE tou.token_public_key = ANY($1))
	//                SELECT tt.id, tt.update_time FROM token_transactions tt JOIN issuer_key_filtered ON tt.id = issuer_key_filtered.token_output_output_created_token_transaction
	//                UNION SELECT tt.id, tt.update_time FROM token_transactions tt JOIN issuer_key_filtered ON tt.id = issuer_key_filtered.token_output_output_spent_token_transaction
	if len(issuerPubKeys) > 0 {
		issuerKeyBytes := make([][]byte, len(issuerPubKeys))
		for i, key := range issuerPubKeys {
			issuerKeyBytes[i] = key.Serialize()
		}
		qb.addFilterCTEAndUnions("issuer_key_filtered", "tou.token_public_key = ANY($%d)", pq.Array(issuerKeyBytes))
	}

	// Handle TokenIdentifiers filter
	// Example query: WITH token_id_filtered AS (SELECT tou.token_output_output_created_token_transaction, tou.token_output_output_spent_token_transaction FROM token_outputs tou WHERE tou.token_identifier = ANY($1))
	//                SELECT tt.id, tt.update_time FROM token_transactions tt JOIN token_id_filtered ON tt.id = token_id_filtered.token_output_output_created_token_transaction
	//                UNION SELECT tt.id, tt.update_time FROM token_transactions tt JOIN token_id_filtered ON tt.id = token_id_filtered.token_output_output_spent_token_transaction
	if len(req.TokenIdentifiers) > 0 {
		qb.addFilterCTEAndUnions("token_id_filtered", "tou.token_identifier = ANY($%d)", pq.Array(req.TokenIdentifiers))
	}

	if len(qb.unions) == 0 {
		return "", nil, fmt.Errorf("no valid filters provided for optimized query")
	}

	// Build the final query with CTEs
	var queryBuilder strings.Builder

	// Add CTEs if any
	if len(qb.ctes) > 0 {
		queryBuilder.WriteString("WITH ")
		queryBuilder.WriteString(strings.Join(qb.ctes, ", "))
		queryBuilder.WriteString(" ")
	}

	// Combine unions
	queryBuilder.WriteString(fmt.Sprintf("SELECT * FROM (%s) combined", strings.Join(qb.unions, " UNION ")))

	// Add ordering, limit, and offset
	queryBuilder.WriteString(" ORDER BY combined.update_time DESC")

	limit := req.GetLimit()
	if limit == 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}
	queryBuilder.WriteString(fmt.Sprintf(" LIMIT $%d", qb.argIndex))
	qb.args = append(qb.args, limit)
	qb.argIndex++

	if req.Offset > 0 {
		queryBuilder.WriteString(fmt.Sprintf(" OFFSET $%d", qb.argIndex))
		qb.args = append(qb.args, req.Offset)
	}

	return queryBuilder.String(), qb.args, nil
}

// queryWithEnt runs an ent-based query for simple cases without complicated filters
func (h *QueryTokenTransactionsHandler) queryWithEnt(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest, db *ent.Client) ([]*ent.TokenTransaction, error) {
	baseQuery := db.TokenTransaction.Query()

	if len(req.TokenTransactionHashes) > 0 {
		baseQuery = baseQuery.Where(tokentransaction.FinalizedTokenTransactionHashIn(req.TokenTransactionHashes...))
	}

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

	query = query.
		WithCreatedOutput().
		WithSpentOutput(func(slq *ent.TokenOutputQuery) {
			slq.WithOutputCreatedTokenTransaction()
		}).
		WithCreate().
		WithMint().
		WithSparkInvoice()

	transactions, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query token transactions: %w", err)
	}

	return transactions, nil
}

// convertTransactionsToResponse converts Ent transactions to protobuf response
func (h *QueryTokenTransactionsHandler) convertTransactionsToResponse(ctx context.Context, transactions []*ent.TokenTransaction, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
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

type queryBuilder struct {
	ctes         []string
	unions       []string
	args         []any
	argIndex     int
	txHashFilter string
}

// addFilterCTEAndUnions adds a CTE and corresponding union queries for a filter condition
func (qb *queryBuilder) addFilterCTEAndUnions(cteName string, whereClause string, argValue any) {
	const baseSelect = `SELECT tt.id, tt.update_time FROM token_transactions tt`

	// Create CTE
	cte := fmt.Sprintf(`%s AS (SELECT tou.token_output_output_created_token_transaction, tou.token_output_output_spent_token_transaction FROM token_outputs tou WHERE %s)`, cteName, fmt.Sprintf(whereClause, qb.argIndex))
	qb.ctes = append(qb.ctes, cte)
	qb.args = append(qb.args, argValue)
	qb.argIndex++

	// Created outputs union
	createdUnion := fmt.Sprintf(`%s JOIN %s ON tt.id = %s.token_output_output_created_token_transaction`, baseSelect, cteName, cteName)
	if qb.txHashFilter != "" {
		createdUnion += fmt.Sprintf(" WHERE %s", qb.txHashFilter)
	}
	qb.unions = append(qb.unions, createdUnion)

	// Spent outputs union
	spentUnion := fmt.Sprintf(`%s JOIN %s ON tt.id = %s.token_output_output_spent_token_transaction`, baseSelect, cteName, cteName)
	if qb.txHashFilter != "" {
		spentUnion += fmt.Sprintf(" WHERE %s", qb.txHashFilter)
	}
	qb.unions = append(qb.unions, spentUnion)
}

func buildTxHashFilter(req *tokenpb.QueryTokenTransactionsRequest, hashFilterArgIndex int) string {
	if len(req.TokenTransactionHashes) > 0 {
		return fmt.Sprintf("tt.finalized_token_transaction_hash = ANY($%d)", hashFilterArgIndex)
	}
	return ""
}
