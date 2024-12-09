package grpc

import (
	"context"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/handler"
)

// SparkServer is the grpc server for the Spark protocol.
// It will be used by the user or Spark service provider.
type SparkServer struct {
	pb.UnimplementedSparkServiceServer
	config *so.Config
}

// NewSparkServer creates a new SparkServer.
func NewSparkServer(config *so.Config) *SparkServer {
	return &SparkServer{config: config}
}

// GenerateDepositAddress generates a deposit address for the given public key.
func (s *SparkServer) GenerateDepositAddress(ctx context.Context, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	depositHandler := handler.DepositHandler{}
	return depositHandler.GenerateDepositAddress(ctx, s.config, req)
}

// StartTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (s *SparkServer) StartTreeCreation(ctx context.Context, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	depositHandler := handler.DepositHandler{}
	return depositHandler.StartTreeCreation(ctx, s.config, req)
}

// SplitNode splits the given node into the given splits.
func (s *SparkServer) SplitNode(ctx context.Context, req *pb.SplitNodeRequest) (*pb.SplitNodeResponse, error) {
	splitHandler := handler.SplitHandler{}
	return splitHandler.SplitNode(ctx, s.config, req)
}

// CompleteTreeCreation verifies the user signature, completes the tree creation and broadcasts the new tree.
func (s *SparkServer) CompleteTreeCreation(ctx context.Context, req *pb.CompleteTreeCreationRequest) (*pb.CompleteTreeCreationResponse, error) {
	depositHandler := handler.DepositHandler{}
	return depositHandler.CompleteTreeCreation(ctx, s.config, req)
}
