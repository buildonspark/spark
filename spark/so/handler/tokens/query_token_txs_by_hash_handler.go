package tokens

import (
	"context"
	"fmt"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
)

type QueryTokenTransactionsByHashHandler struct {
	config *so.Config
}

// NewQueryTokenTxsByHashHandler creates a new NewQueryTokenTxsByHashHandler.
func NewQueryTokenTransactionsByHashHandler(config *so.Config) *QueryTokenTransactionsByHashHandler {
	return &QueryTokenTransactionsByHashHandler{
		config: config,
	}
}

func (h *QueryTokenTransactionsByHashHandler) QueryTokenTransactionsByHash(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	ctx, span := GetTracer().Start(ctx, "QueryTokenTransactionsByHashHandler.QueryTokenTransactionsByHash")
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
		return nil, fmt.Errorf("unable to query token transactions: %w", err)
	}

	return convertTransactionsToResponse(ctx, h.config, transactions, params)
}
