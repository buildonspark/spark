package grpc

import (
	"context"

	"github.com/lightsparkdev/spark/common/logging"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/handler/tokens"
	sotokens "github.com/lightsparkdev/spark/so/tokens"
)

type SparkTokenServer struct {
	tokenpb.UnimplementedSparkTokenServiceServer
	authzConfig authz.Config
	soConfig    *so.Config
	db          *ent.Client
}

func NewSparkTokenServer(authzConfig authz.Config, soConfig *so.Config, db *ent.Client) *SparkTokenServer {
	return &SparkTokenServer{
		authzConfig: authzConfig,
		soConfig:    soConfig,
		db:          db,
	}
}

func (s *SparkTokenServer) StartTransaction(ctx context.Context, req *tokenpb.StartTransactionRequest) (*tokenpb.StartTransactionResponse, error) {
	ctx, _ = logging.WithRequestAttrs(ctx, sotokens.GetProtoTokenTransactionZapAttrs(ctx, req.PartialTokenTransaction)...)
	tokenTransactionHandler := tokens.NewStartTokenTransactionHandler(s.soConfig)
	return tokenTransactionHandler.StartTokenTransaction(ctx, req)
}

// CommitTransaction is called by the client to initiate the coordinated signing process.
func (s *SparkTokenServer) CommitTransaction(ctx context.Context, req *tokenpb.CommitTransactionRequest) (*tokenpb.CommitTransactionResponse, error) {
	ctx, _ = logging.WithRequestAttrs(ctx, sotokens.GetProtoTokenTransactionZapAttrs(ctx, req.FinalTokenTransaction)...)
	signTokenHandler := tokens.NewSignTokenHandler(s.soConfig)
	return signTokenHandler.CommitTransaction(ctx, req)
}

// QueryTokenMetadata returns created token metadata associated with passed in token identifiers or issuer public keys.
func (s *SparkTokenServer) QueryTokenMetadata(ctx context.Context, req *tokenpb.QueryTokenMetadataRequest) (*tokenpb.QueryTokenMetadataResponse, error) {
	queryTokenMetadataHandler := tokens.NewQueryTokenMetadataHandler(s.soConfig)
	return queryTokenMetadataHandler.QueryTokenMetadata(ctx, req)
}

// QueryTokenTransactions returns token transactions with status using native tokenpb protos.
func (s *SparkTokenServer) QueryTokenTransactions(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	queryTokenTransactionsHandler := tokens.NewQueryTokenTransactionsHandler(s.soConfig)
	return queryTokenTransactionsHandler.QueryTokenTransactions(ctx, req)
}

// QueryTokenOutputs returns token outputs with previous transaction data using native tokenpb protos.
func (s *SparkTokenServer) QueryTokenOutputs(ctx context.Context, req *tokenpb.QueryTokenOutputsRequest) (*tokenpb.QueryTokenOutputsResponse, error) {
	queryTokenOutputsHandler := tokens.NewQueryTokenOutputsHandlerWithExpiredTransactions(s.soConfig)
	return queryTokenOutputsHandler.QueryTokenOutputsToken(ctx, req)
}

// FreezeTokens prevents transfer of all outputs owned now and in the future by the provided owner public key.
// Unfreeze undos this operation and re-enables transfers.
func (s *SparkTokenServer) FreezeTokens(
	ctx context.Context,
	req *tokenpb.FreezeTokensRequest,
) (*tokenpb.FreezeTokensResponse, error) {
	freezeTokenHandler := tokens.NewFreezeTokenHandler(s.soConfig)
	return freezeTokenHandler.FreezeTokens(ctx, req)
}

func (s *SparkTokenServer) BroadcastTransaction(ctx context.Context, req *tokenpb.BroadcastTransactionRequest) (*tokenpb.BroadcastTransactionResponse, error) {
	broadcastTokenTransactionHandler := tokens.NewBroadcastTokenHandler(s.soConfig)
	resp, err := broadcastTokenTransactionHandler.BroadcastTokenTransaction(ctx, req)
	return resp, err
}
