package grpc

import (
	"context"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/handler"
	"github.com/lightsparkdev/spark-go/so/helper"
)

// SparkServer is the grpc server for the Spark protocol.
// It will be used by the user or Spark service provider.
type SparkServer struct {
	pb.UnimplementedSparkServiceServer
	config        *so.Config
	onchainHelper helper.OnChainHelper
}

// NewSparkServer creates a new SparkServer.
func NewSparkServer(config *so.Config, onchainHelper helper.OnChainHelper) *SparkServer {
	return &SparkServer{config: config, onchainHelper: onchainHelper}
}

// GenerateDepositAddress generates a deposit address for the given public key.
func (s *SparkServer) GenerateDepositAddress(ctx context.Context, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	depositHandler := handler.NewDepositHandler(s.onchainHelper)
	return depositHandler.GenerateDepositAddress(ctx, s.config, req)
}

// StartTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (s *SparkServer) StartTreeCreation(ctx context.Context, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	depositHandler := handler.NewDepositHandler(s.onchainHelper)
	return depositHandler.StartTreeCreation(ctx, s.config, req)
}

// PrepareSplitAddress prepares the split addresses for the given public keys.
func (s *SparkServer) PrepareSplitAddress(ctx context.Context, req *pb.PrepareSplitAddressRequest) (*pb.PrepareSplitAddressResponse, error) {
	splitHandler := handler.SplitHandler{}
	return splitHandler.PrepareSplitAddress(ctx, s.config, req)
}

// SplitNode splits the given node into the given splits.
func (s *SparkServer) SplitNode(ctx context.Context, req *pb.SplitNodeRequest) (*pb.SplitNodeResponse, error) {
	splitHandler := handler.SplitHandler{}
	return splitHandler.SplitNode(ctx, s.config, req)
}

// FinalizeNodeSignatures verifies the node signatures and updates the node.
func (s *SparkServer) FinalizeNodeSignatures(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	finalizeSignatureHandler := handler.NewFinalizeSignatureHandler(s.config)
	return finalizeSignatureHandler.FinalizeNodeSignatures(ctx, req)
}

// SendTransfer initiates a transfer.
func (s *SparkServer) SendTransfer(ctx context.Context, req *pb.SendTransferRequest) (*pb.SendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.SendTransfer(ctx, req)
}

// QueryPendingTransfers queries the pending transfers to claim.
func (s *SparkServer) QueryPendingTransfers(ctx context.Context, req *pb.QueryPendingTransfersRequest) (*pb.QueryPendingTransfersResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.QueryPendingTransfers(ctx, req)
}
