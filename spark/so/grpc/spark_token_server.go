package grpc

import (
	"context"

	"github.com/lightsparkdev/spark/common/logging"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/handler/tokens"
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

func (s *SparkTokenServer) StartTransaction(
	ctx context.Context,
	req *tokenpb.StartTransactionRequest,
) (*tokenpb.StartTransactionResponse, error) {
	ctx, _ = logging.WithIdentityPubkey(ctx, req.IdentityPublicKey)
	tokenTransactionHandler := tokens.NewStartTokenTransactionHandlerWithPreemption(s.soConfig)
	resp, err := tokenTransactionHandler.StartTokenTransaction(ctx, req)
	return resp, err
}

// CommitTransaction is called by the client to initiate the coordinated signing process.
func (s *SparkTokenServer) CommitTransaction(
	ctx context.Context,
	req *tokenpb.CommitTransactionRequest,
) (*tokenpb.CommitTransactionResponse, error) {
	signTokenHandler := tokens.NewSignTokenHandler(s.soConfig)
	resp, err := signTokenHandler.CommitTransaction(ctx, req)
	return resp, err
}

// QueryTokenMetadata returns created token metadata associated with passed in token identifiers or issuer public keys.
func (s *SparkTokenServer) QueryTokenMetadata(ctx context.Context, req *tokenpb.QueryTokenMetadataRequest) (*tokenpb.QueryTokenMetadataResponse, error) {
	queryTokenHandler := tokens.NewQueryTokenHandler(s.soConfig)
	resp, err := queryTokenHandler.QueryTokenMetadata(ctx, req)
	return resp, err
}

// QueryTokenTransactions returns token transactions with status using native tokenpb protos.
func (s *SparkTokenServer) QueryTokenTransactions(ctx context.Context, req *tokenpb.QueryTokenTransactionsRequest) (*tokenpb.QueryTokenTransactionsResponse, error) {
	queryTokenHandler := tokens.NewQueryTokenHandler(s.soConfig)
	resp, err := queryTokenHandler.QueryTokenTransactionsToken(ctx, req)
	return resp, err
}

// QueryTokenOutputs returns token outputs with previous transaction data using native tokenpb protos.
func (s *SparkTokenServer) QueryTokenOutputs(ctx context.Context, req *tokenpb.QueryTokenOutputsRequest) (*tokenpb.QueryTokenOutputsResponse, error) {
	queryTokenHandler := tokens.NewQueryTokenHandlerWithExpiredTransactions(s.soConfig)
	resp, err := queryTokenHandler.QueryTokenOutputsToken(ctx, req)
	return resp, err
}

// FreezeTokens prevents transfer of all outputs owned now and in the future by the provided owner public key.
// Unfreeze undos this operation and re-enables transfers.
func (s *SparkTokenServer) FreezeTokens(
	ctx context.Context,
	req *tokenpb.FreezeTokensRequest,
) (*tokenpb.FreezeTokensResponse, error) {
	freezeTokenHandler := tokens.NewFreezeTokenHandler(s.soConfig)
	sparkRes, err := freezeTokenHandler.FreezeTokens(ctx, req)
	if err != nil {
		return nil, err
	}
	return sparkRes, nil
}
