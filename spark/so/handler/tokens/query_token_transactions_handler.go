package tokens

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	"go.uber.org/zap"

	sparkerrors "github.com/lightsparkdev/spark/so/errors"
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

const (
	maxTokenTransactionFilterValues = 500
	maxTokenTransactionPageSize     = 100
	defaultTokenTransactionPageSize = 50
)

type queryParams struct {
	outputIDs              []string
	ownerPublicKeys        []keys.Public
	issuerPublicKeys       []keys.Public
	tokenIdentifiers       [][]byte
	tokenTransactionHashes [][]byte
	order                  sparkpb.Order
	limit                  int64
	offset                 int64
}

func normalizeQueryParams(req *tokenpb.QueryTokenTransactionsRequest) (*queryParams, error) {
	limit := req.GetLimit()
	if limit == 0 {
		limit = defaultTokenTransactionPageSize
	} else if limit > maxTokenTransactionPageSize {
		limit = maxTokenTransactionPageSize
	}

	ownerPubKeys, err := keys.ParsePublicKeys(req.GetOwnerPublicKeys())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner public keys: %w", err)
	}

	issuerPubKeys, err := keys.ParsePublicKeys(req.GetIssuerPublicKeys())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner public keys: %w", err)
	}

	return &queryParams{
		outputIDs:              req.OutputIds,
		ownerPublicKeys:        ownerPubKeys,
		issuerPublicKeys:       issuerPubKeys,
		tokenIdentifiers:       req.GetTokenIdentifiers(),
		tokenTransactionHashes: req.GetTokenTransactionHashes(),
		order:                  req.GetOrder(),
		limit:                  limit,
		offset:                 req.Offset,
	}, nil
}

// NewQueryTokenTransactionsHandler creates a new QueryTokenTransactionsHandler.
func NewQueryTokenTransactionsHandler(config *so.Config) *QueryTokenTransactionsHandler {
	return &QueryTokenTransactionsHandler{
		config:                     config,
		includeExpiredTransactions: false,
	}
}

// QueryTokenTransactions returns SO provided data about specific token transactions alosng with their status.
// Allows caller to specify data to be returned related to:
// a) transactions associated with a particular set of output ids
// b) transactions associated with a particular set of transaction hashes
// c) all transactions associated with a particular token public key
func (h *QueryTokenTransactionsHandler) QueryTokenTransactions(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	ctx, span := GetTracer().Start(ctx, "QueryTokenTransactionsHandler.queryTokenTransactionsInternal")
	defer span.End()

	if err := validateQueryTokenTransactionsRequest(req); err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	params, err := normalizeQueryParams(req)
	if err != nil {
		return nil, err
	}
	var transactions []*ent.TokenTransaction

	// Check if we should use the optimized UNION query
	useOptimizedQuery := h.shouldUseOptimizedQuery(params)
	if useOptimizedQuery {
		transactions, err = h.queryWithRawSql(ctx, params, db)
		if err != nil {
			return nil, fmt.Errorf("failed to query token transactions with raw sql: %w", err)
		}
	} else {
		transactions, err = h.queryWithEnt(ctx, params, db)
		if err != nil {
			return nil, fmt.Errorf("failed to query token transactions with ent: %w", err)
		}
	}

	return h.convertTransactionsToResponse(ctx, transactions, params)
}

func validateQueryTokenTransactionsRequest(req *tokenpb.QueryTokenTransactionsRequest) error {
	if len(req.OutputIds) > maxTokenTransactionFilterValues {
		return sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many output ids in filter: got %d, max %d", len(req.OutputIds), maxTokenTransactionFilterValues),
		)
	}

	if len(req.OwnerPublicKeys) > maxTokenTransactionFilterValues {
		return sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many owner public keys in filter: got %d, max %d", len(req.OwnerPublicKeys), maxTokenTransactionFilterValues),
		)
	}

	if len(req.IssuerPublicKeys) > maxTokenTransactionFilterValues {
		return sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many issuer public keys in filter: got %d, max %d", len(req.IssuerPublicKeys), maxTokenTransactionFilterValues),
		)
	}

	if len(req.TokenIdentifiers) > maxTokenTransactionFilterValues {
		return sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many token identifiers in filter: got %d, max %d", len(req.TokenIdentifiers), maxTokenTransactionFilterValues),
		)
	}

	if len(req.TokenTransactionHashes) > maxTokenTransactionFilterValues {
		return sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many token transaction hashes in filter: got %d, max %d", len(req.TokenTransactionHashes), maxTokenTransactionFilterValues),
		)
	}

	return nil
}

// shouldUseOptimizedQuery determines if we should use the optimized UNION-based query
func (h *QueryTokenTransactionsHandler) shouldUseOptimizedQuery(params *queryParams) bool {
	// Use optimized query when we have filters that require token_outputs joins
	hasOutputFilters := len(params.outputIDs) > 0 ||
		len(params.ownerPublicKeys) > 0 ||
		len(params.issuerPublicKeys) > 0 ||
		len(params.tokenIdentifiers) > 0
	return hasOutputFilters
}

// queryTokenTransactionsRawSql uses raw SQL with UNION for better performance
func (h *QueryTokenTransactionsHandler) queryWithRawSql(ctx context.Context, params *queryParams, db *ent.Client) ([]*ent.TokenTransaction, error) {
	ctx, span := GetTracer().Start(ctx, "QueryTokenTransactionsHandler.queryTokenTransactionsOptimized")
	defer span.End()

	// Build the optimized UNION query
	query, args, err := h.buildOptimizedQuery(params)
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

	// Scan the results into a simple struct for ID and create_time
	type transactionResult struct {
		ID         uuid.UUID `json:"id"`
		CreateTime time.Time `json:"create_time"`
	}

	var results []transactionResult
	for rows.Next() {
		var result transactionResult
		if err := rows.Scan(&result.ID, &result.CreateTime); err != nil {
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
func (h *QueryTokenTransactionsHandler) buildOptimizedQuery(params *queryParams) (string, []any, error) {
	// Initialize query builder
	qb := &queryBuilder{
		args:     make([]any, 0),
		argIndex: 1,
	}

	ownerPubKeys := params.ownerPublicKeys
	issuerPubKeys := params.issuerPublicKeys

	// Build a single CTE with ALL filters combined
	// This ensures the same output satisfies all conditions
	var whereConditions []string

	// Handle OutputIds filter
	if len(params.outputIDs) > 0 {
		outputUUIDs, err := uuids.ParseSlice(params.outputIDs)
		if err != nil {
			return "", nil, fmt.Errorf("invalid output ID format: %w", err)
		}
		whereConditions = append(whereConditions, fmt.Sprintf("tou.id = ANY($%d)", qb.argIndex))
		qb.args = append(qb.args, pq.Array(outputUUIDs))
		qb.argIndex++
	}

	// Handle OwnerPublicKeys filter
	if len(ownerPubKeys) > 0 {
		ownerKeyBytes := make([][]byte, len(ownerPubKeys))
		for i, key := range ownerPubKeys {
			ownerKeyBytes[i] = key.Serialize()
		}
		whereConditions = append(whereConditions, fmt.Sprintf("tou.owner_public_key = ANY($%d)", qb.argIndex))
		qb.args = append(qb.args, pq.Array(ownerKeyBytes))
		qb.argIndex++
	}

	// Handle IssuerPublicKeys filter
	if len(issuerPubKeys) > 0 {
		issuerKeyBytes := make([][]byte, len(issuerPubKeys))
		for i, key := range issuerPubKeys {
			issuerKeyBytes[i] = key.Serialize()
		}
		whereConditions = append(whereConditions, fmt.Sprintf("tou.token_public_key = ANY($%d)", qb.argIndex))
		qb.args = append(qb.args, pq.Array(issuerKeyBytes))
		qb.argIndex++
	}

	// Handle TokenIdentifiers filter
	if len(params.tokenIdentifiers) > 0 {
		whereConditions = append(whereConditions, fmt.Sprintf("tou.token_identifier = ANY($%d)", qb.argIndex))
		qb.args = append(qb.args, pq.Array(params.tokenIdentifiers))
		qb.argIndex++
	}

	if len(whereConditions) == 0 {
		return "", nil, fmt.Errorf("no valid filters provided for optimized query")
	}

	// Build the CTE with all conditions combined with AND
	cteWhere := strings.Join(whereConditions, " AND ")
	cte := fmt.Sprintf(`filtered_outputs AS (
		SELECT
			tou.token_output_output_created_token_transaction,
			tou.token_output_output_spent_token_transaction
		FROM token_outputs tou
		WHERE %s
	)`, cteWhere)

	// Build transaction hash filter if provided
	var txHashFilter string
	if len(params.tokenTransactionHashes) > 0 {
		txHashFilter = fmt.Sprintf(" WHERE tt.finalized_token_transaction_hash = ANY($%d)", qb.argIndex)
		qb.args = append(qb.args, pq.Array(params.tokenTransactionHashes))
		qb.argIndex++
	}

	// Build the final query with CTE
	var queryBuilder strings.Builder
	queryBuilder.WriteString("WITH ")
	queryBuilder.WriteString(cte)
	queryBuilder.WriteString(" SELECT DISTINCT * FROM (")

	// UNION: transactions that created the filtered outputs OR spent the filtered outputs
	queryBuilder.WriteString("SELECT tt.id, tt.create_time FROM token_transactions tt ")
	queryBuilder.WriteString("JOIN filtered_outputs ON tt.id = filtered_outputs.token_output_output_created_token_transaction")
	queryBuilder.WriteString(txHashFilter)
	queryBuilder.WriteString(" UNION ALL ")
	queryBuilder.WriteString("SELECT tt.id, tt.create_time FROM token_transactions tt ")
	queryBuilder.WriteString("JOIN filtered_outputs ON tt.id = filtered_outputs.token_output_output_spent_token_transaction")
	queryBuilder.WriteString(txHashFilter)

	queryBuilder.WriteString(") combined")

	// Add ordering, limit, and offset
	if params.order == sparkpb.Order_ASCENDING {
		queryBuilder.WriteString(" ORDER BY combined.create_time ASC")
	} else {
		queryBuilder.WriteString(" ORDER BY combined.create_time DESC")
	}

	queryBuilder.WriteString(fmt.Sprintf(" LIMIT $%d", qb.argIndex))
	qb.args = append(qb.args, params.limit)
	qb.argIndex++

	if params.offset > 0 {
		queryBuilder.WriteString(fmt.Sprintf(" OFFSET $%d", qb.argIndex))
		qb.args = append(qb.args, params.offset)
	}

	return queryBuilder.String(), qb.args, nil
}

// queryWithEnt runs an ent-based query for simple cases without complicated filters
func (h *QueryTokenTransactionsHandler) queryWithEnt(ctx context.Context, params *queryParams, db *ent.Client) ([]*ent.TokenTransaction, error) {
	baseQuery := db.TokenTransaction.Query()

	if len(params.tokenTransactionHashes) > 0 {
		baseQuery = baseQuery.Where(tokentransaction.FinalizedTokenTransactionHashIn(params.tokenTransactionHashes...))
	}

	query := baseQuery
	if params.order == sparkpb.Order_ASCENDING {
		query = query.Order(ent.Asc(tokentransaction.FieldCreateTime))
	} else {
		query = query.Order(ent.Desc(tokentransaction.FieldCreateTime))
	}

	query = query.Limit(int(params.limit))

	if params.offset > 0 {
		query = query.Offset(int(params.offset))
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
func (h *QueryTokenTransactionsHandler) convertTransactionsToResponse(ctx context.Context, transactions []*ent.TokenTransaction, params *queryParams) (*tokenpb.QueryTokenTransactionsResponse, error) {
	transactionsWithStatus := make([]*tokenpb.TokenTransactionWithStatus, 0, len(transactions))
	for _, transaction := range transactions {
		status := protoconverter.ConvertTokenTransactionStatusToTokenPb(transaction.Status)

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
					RevocationSecret: spentOutput.SpentRevocationSecret.Serialize(),
				}
			}
			transactionWithStatus.ConfirmationMetadata = &tokenpb.TokenTransactionConfirmationMetadata{
				SpentTokenOutputsMetadata: spentTokenOutputsMetadata,
			}
		}
		transactionsWithStatus = append(transactionsWithStatus, transactionWithStatus)
	}

	var nextOffset int64
	if len(transactions) == int(params.limit) {
		nextOffset = params.offset + int64(len(transactions))
	} else {
		nextOffset = -1
	}

	return &tokenpb.QueryTokenTransactionsResponse{
		TokenTransactionsWithStatus: transactionsWithStatus,
		Offset:                      nextOffset,
	}, nil
}

type queryBuilder struct {
	args     []any
	argIndex int
}
