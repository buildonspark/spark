package tokens

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	"github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"

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
	config *so.Config
}

const (
	maxTokenTransactionFilterValues = 500
	maxTokenTransactionHashValues   = 100
	maxTokenTransactionPageSize     = 100
	defaultTokenTransactionPageSize = 50
)

// queryBackend represents the database query implementation used.
type queryBackend string

const (
	queryBackendRawSQL queryBackend = "raw_sql"
	queryBackendEnt    queryBackend = "ent"
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
	afterID                *uuid.UUID
	beforeID               *uuid.UUID
	useCursorPagination    bool
	direction              sparkpb.Direction
	cursorProvided         bool
}

// NewQueryTokenTransactionsHandler creates a new QueryTokenTransactionsHandler.
func NewQueryTokenTransactionsHandler(config *so.Config) *QueryTokenTransactionsHandler {
	return &QueryTokenTransactionsHandler{
		config: config,
	}
}

func (h *QueryTokenTransactionsHandler) QueryTokenTransactionsByHash(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	ctx, span := GetTracer().Start(ctx, "QueryTokenTransactionsByHashHandler.QueryTokenTransactionsByHash")
	defer span.End()

	if err := validateQueryTokenTransactionsRequest(req); err != nil {
		return nil, err
	}

	params, err := normalizeQueryParams(req)
	if err != nil {
		return nil, err
	}

	metricsRecorder := newQueryMetricsRecorder(params, queryBackendEnt, queryTypeByHash)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	query := db.TokenTransaction.Query().Where(tokentransaction.FinalizedTokenTransactionHashIn(params.tokenTransactionHashes...))

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
		metricsRecorder.record(ctx, 0, err)
		return nil, fmt.Errorf("unable to query token transactions: %w", err)
	}

	resp, err := convertTransactionsToResponse(ctx, h.config, transactions, params)
	metricsRecorder.record(ctx, len(transactions), err)
	return resp, err
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

	params, err := normalizeQueryParams(req)
	if err != nil {
		return nil, err
	}

	requestedLimit := params.limit
	if params.useCursorPagination {
		params.limit = requestedLimit + 1
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var transactions []*ent.TokenTransaction

	requestQueryBackend := h.determineQueryBackend(params)
	metricsRecorder := newQueryMetricsRecorder(params, requestQueryBackend, queryTypeByFilters)

	if requestQueryBackend == queryBackendRawSQL {
		transactions, err = h.queryWithRawSql(ctx, params, db)
		if err != nil {
			metricsRecorder.record(ctx, 0, err)
			return nil, fmt.Errorf("failed to query token transactions with raw sql: %w", err)
		}
	} else {
		transactions, err = h.queryWithEnt(ctx, params, db)
		if err != nil {
			metricsRecorder.record(ctx, 0, err)
			return nil, fmt.Errorf("failed to query token transactions with ent: %w", err)
		}
	}

	params.limit = requestedLimit
	resp, err := convertTransactionsToResponse(ctx, h.config, transactions, params)
	metricsRecorder.record(ctx, len(transactions), err)
	return resp, err
}

// determineQueryBackend determines the query backend to use based on the query parameters
// We use the raw SQL query when we have filters that require token_outputs joins
func (h *QueryTokenTransactionsHandler) determineQueryBackend(params *queryParams) queryBackend {
	hasOutputFilters := len(params.outputIDs) > 0 ||
		len(params.ownerPublicKeys) > 0 ||
		len(params.issuerPublicKeys) > 0 ||
		len(params.tokenIdentifiers) > 0
	if hasOutputFilters {
		return queryBackendRawSQL
	}
	return queryBackendEnt
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

	//nolint:forbidigo // We have to use this API to run the optimized query, since it's a string.
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

	// Add cursor filter if using cursor pagination
	if params.afterID != nil {
		queryBuilder.WriteString(fmt.Sprintf(" WHERE combined.id > $%d", qb.argIndex))
		qb.args = append(qb.args, *params.afterID)
		qb.argIndex++
	} else if params.beforeID != nil {
		queryBuilder.WriteString(fmt.Sprintf(" WHERE combined.id < $%d", qb.argIndex))
		qb.args = append(qb.args, *params.beforeID)
		qb.argIndex++
	}

	// When using cursor pagination, order by ID in the direction that matches the cursor filter.
	// NEXT direction uses afterID (id > cursor), so we need ASC order to get items after cursor.
	// PREVIOUS direction uses beforeID (id < cursor), so we need DESC order to get items before cursor.
	if params.useCursorPagination {
		if params.direction == sparkpb.Direction_PREVIOUS {
			queryBuilder.WriteString(" ORDER BY combined.id DESC")
		} else {
			queryBuilder.WriteString(" ORDER BY combined.id ASC")
		}
	} else if params.order == sparkpb.Order_ASCENDING {
		queryBuilder.WriteString(" ORDER BY combined.create_time ASC, combined.id ASC")
	} else {
		queryBuilder.WriteString(" ORDER BY combined.create_time DESC, combined.id DESC")
	}

	queryBuilder.WriteString(fmt.Sprintf(" LIMIT $%d", qb.argIndex))
	qb.args = append(qb.args, params.limit)
	qb.argIndex++

	if params.offset > 0 && !params.useCursorPagination {
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

	if params.afterID != nil {
		baseQuery = baseQuery.Where(tokentransaction.IDGT(*params.afterID))
	} else if params.beforeID != nil {
		baseQuery = baseQuery.Where(tokentransaction.IDLT(*params.beforeID))
	}

	query := baseQuery
	// When using cursor pagination, order by ID in the direction that matches the cursor filter.
	// NEXT direction uses afterID (id > cursor), so we need ASC order to get items after cursor.
	// PREVIOUS direction uses beforeID (id < cursor), so we need DESC order to get items before cursor.
	if params.useCursorPagination {
		if params.direction == sparkpb.Direction_PREVIOUS {
			query = query.Order(ent.Desc(tokentransaction.FieldID))
		} else {
			query = query.Order(ent.Asc(tokentransaction.FieldID))
		}
	} else if params.order == sparkpb.Order_ASCENDING {
		query = query.Order(ent.Asc(tokentransaction.FieldCreateTime), ent.Asc(tokentransaction.FieldID))
	} else {
		query = query.Order(ent.Desc(tokentransaction.FieldCreateTime), ent.Desc(tokentransaction.FieldID))
	}

	query = query.Limit(int(params.limit))

	if params.offset > 0 && !params.useCursorPagination {
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
func convertTransactionsToResponse(ctx context.Context, config *so.Config, transactions []*ent.TokenTransaction, params *queryParams) (*tokenpb.QueryTokenTransactionsResponse, error) {
	hasMoreResults := len(transactions) > int(params.limit)
	isBackward := params.useCursorPagination && params.direction == sparkpb.Direction_PREVIOUS

	resultTransactions := transactions
	if hasMoreResults {
		resultTransactions = transactions[:params.limit]
	}

	// For backward pagination, reverse the results to restore the original sort order
	if isBackward {
		for i, j := 0, len(resultTransactions)-1; i < j; i, j = i+1, j-1 {
			resultTransactions[i], resultTransactions[j] = resultTransactions[j], resultTransactions[i]
		}
	}

	transactionsWithStatus := make([]*tokenpb.TokenTransactionWithStatus, 0, len(resultTransactions))
	for _, transaction := range resultTransactions {
		status := protoconverter.ConvertTokenTransactionStatusToTokenPb(transaction.Status)

		transactionProto, err := transaction.MarshalProto(ctx, config)
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

	resp := &tokenpb.QueryTokenTransactionsResponse{
		TokenTransactionsWithStatus: transactionsWithStatus,
	}

	if params.useCursorPagination {
		var hasNextPage, hasPreviousPage bool
		cursorProvided := params.cursorProvided
		if isBackward {
			hasNextPage = cursorProvided
			hasPreviousPage = hasMoreResults
		} else {
			hasNextPage = hasMoreResults
			hasPreviousPage = cursorProvided
		}
		pageResponse := &sparkpb.PageResponse{
			HasNextPage:     hasNextPage,
			HasPreviousPage: hasPreviousPage,
		}

		if len(resultTransactions) > 0 {
			firstID := resultTransactions[0].ID
			pageResponse.PreviousCursor = base64.RawURLEncoding.EncodeToString(firstID[:])

			lastID := resultTransactions[len(resultTransactions)-1].ID
			pageResponse.NextCursor = base64.RawURLEncoding.EncodeToString(lastID[:])
		}

		resp.PageResponse = pageResponse
	} else {
		if len(resultTransactions) == int(params.limit) {
			resp.Offset = params.offset + int64(len(resultTransactions))
		} else {
			resp.Offset = -1
		}
	}

	return resp, nil
}

type queryBuilder struct {
	args     []any
	argIndex int
}

func normalizeQueryParams(req *tokenpb.QueryTokenTransactionsRequest) (*queryParams, error) {
	limit := req.GetLimit()
	if limit == 0 {
		limit = defaultTokenTransactionPageSize
	} else if limit > maxTokenTransactionPageSize {
		limit = maxTokenTransactionPageSize
	}

	if req.GetByTxHash() != nil {
		return &queryParams{
			tokenTransactionHashes: req.GetByTxHash().TokenTransactionHashes,
			order:                  req.GetOrder(),
			limit:                  limit,
			offset:                 req.Offset,
		}, nil
	}

	if req.GetByFilters() != nil {
		ownerPubKeys, err := keys.ParsePublicKeys(req.GetByFilters().GetOwnerPublicKeys())
		if err != nil {
			return nil, fmt.Errorf("failed to parse owner public keys: %w", err)
		}

		issuerPubKeys, err := keys.ParsePublicKeys(req.GetByFilters().GetIssuerPublicKeys())
		if err != nil {
			return nil, fmt.Errorf("failed to parse issuer public keys: %w", err)
		}

		params := &queryParams{
			outputIDs:        req.GetByFilters().OutputIds,
			ownerPublicKeys:  ownerPubKeys,
			issuerPublicKeys: issuerPubKeys,
			tokenIdentifiers: req.GetByFilters().TokenIdentifiers,
			order:            req.GetOrder(),
			limit:            limit,
			offset:           req.Offset,
		}

		if pageRequest := req.GetByFilters().GetPageRequest(); pageRequest != nil {
			params.useCursorPagination = true
			params.direction = pageRequest.GetDirection()
			if pageRequest.GetPageSize() > 0 {
				params.limit = min(int64(pageRequest.GetPageSize()), maxTokenTransactionPageSize)
			}
			if cursor := pageRequest.GetCursor(); cursor != "" {
				params.cursorProvided = true
				cursorBytes, err := base64.RawURLEncoding.DecodeString(cursor)
				if err != nil {
					cursorBytes, err = base64.URLEncoding.DecodeString(cursor)
					if err != nil {
						return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor: %w", err))
					}
				}
				id, err := uuid.FromBytes(cursorBytes)
				if err != nil {
					return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor: %w", err))
				}
				if params.direction != sparkpb.Direction_PREVIOUS {
					params.afterID = &id
				} else {
					params.beforeID = &id
				}
			}
		}

		return params, nil
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
