package grpc

import (
	"context"
	"log"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/handler"
	"github.com/lightsparkdev/spark-go/so/helper"
	"google.golang.org/protobuf/types/known/emptypb"
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

// StartSendTransfer initiates a transfer from sender.
func (s *SparkServer) StartSendTransfer(ctx context.Context, req *pb.StartSendTransferRequest) (*pb.StartSendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.StartSendTransfer(ctx, req)
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

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (s *SparkServer) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) (*emptypb.Empty, error) {
	transferHander := handler.NewTransferHandler(s.config)
	err := transferHander.ClaimTransferTweakKeys(ctx, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (s *SparkServer) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.ClaimTransferSignRefunds(ctx, req)
}

// AggregateNodes aggregates the given nodes.
func (s *SparkServer) AggregateNodes(ctx context.Context, req *pb.AggregateNodesRequest) (*pb.AggregateNodesResponse, error) {
	aggregateHandler := handler.NewAggregateHandler(s.config)
	return aggregateHandler.AggregateNodes(ctx, req)
}

// StorePreimageShare stores the preimage share for the given payment hash.
func (s *SparkServer) StorePreimageShare(ctx context.Context, req *pb.StorePreimageShareRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	err := lightningHandler.StorePreimageShare(ctx, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// GetSigningCommitments gets the signing commitments for the given node ids.
func (s *SparkServer) GetSigningCommitments(ctx context.Context, req *pb.GetSigningCommitmentsRequest) (*pb.GetSigningCommitmentsResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.GetSigningCommitments(ctx, req)
}

// GetPreimage gets the preimage for the given payment hash.
func (s *SparkServer) GetPreimage(ctx context.Context, req *pb.GetPreimageRequest) (*pb.GetPreimageResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.GetPreimage(ctx, req)
}

// PrepareTreeAddress prepares the tree address for the given public key.
func (s *SparkServer) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	treeHandler := handler.NewTreeCreationHandler(s.config, s.onchainHelper)
	result, err := treeHandler.PrepareTreeAddress(ctx, req)
	if err != nil {
		log.Printf("failed to prepare tree address: %v", err)
	}
	return result, err
}

// CreateTree creates a tree from user input and signs the transactions in the tree.
func (s *SparkServer) CreateTree(ctx context.Context, req *pb.CreateTreeRequest) (*pb.CreateTreeResponse, error) {
	treeHandler := handler.NewTreeCreationHandler(s.config, s.onchainHelper)
	result, err := treeHandler.CreateTree(ctx, req)
	if err != nil {
		log.Printf("failed to create tree: %v", err)
	}
	return result, err
}
