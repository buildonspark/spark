package grpc

import (
	"context"

	"github.com/lightsparkdev/spark/common/logging"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/handler/tokens"
	sotokens "github.com/lightsparkdev/spark/so/tokens"
)

type SparkTokenInternalServer struct {
	tokeninternalpb.UnimplementedSparkTokenInternalServiceServer
	soConfig *so.Config
	db       *ent.Client
}

func NewSparkTokenInternalServer(soConfig *so.Config, db *ent.Client) *SparkTokenInternalServer {
	return &SparkTokenInternalServer{
		soConfig: soConfig,
		db:       db,
	}
}

func (s *SparkTokenInternalServer) PrepareTransaction(ctx context.Context, req *tokeninternalpb.PrepareTransactionRequest) (*tokeninternalpb.PrepareTransactionResponse, error) {
	prepareHandler := tokens.NewInternalPrepareTokenHandler(s.soConfig)
	ctx, _ = logging.WithRequestAttrs(ctx, sotokens.GetProtoTokenTransactionZapAttrs(ctx, req.FinalTokenTransaction)...)
	resp, err := prepareHandler.PrepareTokenTransactionInternal(ctx, req)
	return resp, err
}

func (s *SparkTokenInternalServer) SignTokenTransactionFromCoordination(
	ctx context.Context,
	req *tokeninternalpb.SignTokenTransactionFromCoordinationRequest,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	ctx, _ = logging.WithRequestAttrs(ctx, sotokens.GetProtoTokenTransactionZapAttrs(ctx, req.FinalTokenTransaction)...)
	tx, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, sotokens.FormatErrorWithTransactionProto("failed to fetch transaction", req.FinalTokenTransaction, err)
	}

	internalSignTokenHandler := tokens.NewInternalSignTokenHandler(s.soConfig)
	var ttxoSignatures []*tokenpb.SignatureWithIndex
	if req.InputTtxoSignaturesPerOperator != nil {
		ttxoSignatures = req.InputTtxoSignaturesPerOperator.TtxoSignatures
	}
	sigBytes, err := internalSignTokenHandler.SignAndPersistTokenTransaction(
		ctx,
		tx,
		req.FinalTokenTransaction,
		req.FinalTokenTransactionHash,
		ttxoSignatures,
	)
	if err != nil {
		return nil, err
	}

	return &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
		SparkOperatorSignature: sigBytes,
	}, nil
}

func (s *SparkTokenInternalServer) ExchangeRevocationSecretsShares(
	ctx context.Context,
	req *tokeninternalpb.ExchangeRevocationSecretsSharesRequest,
) (*tokeninternalpb.ExchangeRevocationSecretsSharesResponse, error) {
	internalTokenTransactionHandler := tokens.NewInternalSignTokenHandler(s.soConfig)
	ctx, _ = logging.WithRequestAttrs(ctx, sotokens.GetProtoTokenTransactionZapAttrs(ctx, req.FinalTokenTransaction)...)
	return internalTokenTransactionHandler.ExchangeRevocationSecretsShares(ctx, req)
}

func (s *SparkTokenInternalServer) BroadcastTokenTransactionInternal(ctx context.Context, req *tokeninternalpb.BroadcastTransactionInternalRequest) (*tokeninternalpb.BroadcastTransactionInternalResponse, error) {
	internalTokenTransactionHandler := tokens.NewInternalBroadcastTokenHandler(s.soConfig)
	ctx, _ = logging.WithRequestAttrs(ctx, sotokens.GetProtoTokenTransactionZapAttrs(ctx, req.FinalTokenTransaction)...)
	return internalTokenTransactionHandler.BroadcastTokenTransactionInternal(ctx, req)
}
