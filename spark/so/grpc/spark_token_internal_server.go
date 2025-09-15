package grpc

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/handler/tokens"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/protoconverter"
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
	prepareHandler := tokens.NewInternalPrepareTokenHandlerWithPreemption(s.soConfig)
	resp, err := prepareHandler.PrepareTokenTransactionInternal(ctx, req)
	return resp, err
}

func (s *SparkTokenInternalServer) SignTokenTransactionFromCoordination(
	ctx context.Context,
	req *tokeninternalpb.SignTokenTransactionFromCoordinationRequest,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	ctx, _ = logging.WithAttrs(ctx, sotokens.GetFinalizedTokenTransactionAttrs(req.FinalTokenTransactionHash)...)
	tx, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch transaction: %w", err)
	}

	// Convert proto signatures to []*sparkpb.OperatorSpecificOwnerSignature
	operatorSpecificSignatures := make([]*sparkpb.OperatorSpecificOwnerSignature, 0)
	for _, sigWithIndex := range req.InputTtxoSignaturesPerOperator.TtxoSignatures {
		operatorSpecificSignatures = append(operatorSpecificSignatures, &sparkpb.OperatorSpecificOwnerSignature{
			OwnerSignature: protoconverter.SparkSignatureWithIndexFromTokenProto(sigWithIndex),
			Payload: &sparkpb.OperatorSpecificTokenTransactionSignablePayload{
				FinalTokenTransactionHash: req.FinalTokenTransactionHash,
				OperatorIdentityPublicKey: req.InputTtxoSignaturesPerOperator.OperatorIdentityPublicKey,
			},
		})
	}

	internalSignTokenHandler := tokens.NewInternalSignTokenHandler(s.soConfig)
	sigBytes, err := internalSignTokenHandler.SignAndPersistTokenTransaction(ctx, tx, req.FinalTokenTransactionHash, operatorSpecificSignatures)
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
	return internalTokenTransactionHandler.ExchangeRevocationSecretsShares(ctx, req)
}
