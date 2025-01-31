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

var emptyResponse = &emptypb.Empty{}

// NewSparkServer creates a new SparkServer.
func NewSparkServer(config *so.Config, onchainHelper helper.OnChainHelper) *SparkServer {
	return &SparkServer{config: config, onchainHelper: onchainHelper}
}

// GenerateDepositAddress generates a deposit address for the given public key.
func (s *SparkServer) GenerateDepositAddress(ctx context.Context, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	depositHandler := handler.NewDepositHandler(s.onchainHelper, s.config)
	return wrapWithGRPCError(depositHandler.GenerateDepositAddress(ctx, s.config, req))
}

// StartTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (s *SparkServer) StartTreeCreation(ctx context.Context, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	depositHandler := handler.NewDepositHandler(s.onchainHelper, s.config)
	return wrapWithGRPCError(depositHandler.StartTreeCreation(ctx, s.config, req))
}

// FinalizeNodeSignatures verifies the node signatures and updates the node.
func (s *SparkServer) FinalizeNodeSignatures(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	finalizeSignatureHandler := handler.NewFinalizeSignatureHandler(s.config)
	return finalizeSignatureHandler.FinalizeNodeSignatures(ctx, req)
}

// StartSendTransfer initiates a transfer from sender.
func (s *SparkServer) StartSendTransfer(ctx context.Context, req *pb.StartSendTransferRequest) (*pb.StartSendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.onchainHelper, s.config)
	return transferHander.StartSendTransfer(ctx, req)
}

// CompleteSendTransfer completes a transfer from sender.
func (s *SparkServer) CompleteSendTransfer(ctx context.Context, req *pb.CompleteSendTransferRequest) (*pb.CompleteSendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.onchainHelper, s.config)
	return wrapWithGRPCError(transferHander.CompleteSendTransfer(ctx, req))
}

// QueryPendingTransfers queries the pending transfers to claim.
func (s *SparkServer) QueryPendingTransfers(ctx context.Context, req *pb.QueryPendingTransfersRequest) (*pb.QueryPendingTransfersResponse, error) {
	transferHander := handler.NewTransferHandler(s.onchainHelper, s.config)
	return wrapWithGRPCError(transferHander.QueryPendingTransfers(ctx, req))
}

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (s *SparkServer) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) (*emptypb.Empty, error) {
	transferHander := handler.NewTransferHandler(s.onchainHelper, s.config)
	return wrapWithGRPCError(emptyResponse, transferHander.ClaimTransferTweakKeys(ctx, req))
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (s *SparkServer) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	transferHander := handler.NewTransferHandler(s.onchainHelper, s.config)
	return wrapWithGRPCError(transferHander.ClaimTransferSignRefunds(ctx, req))
}

// AggregateNodes aggregates the given nodes.
func (s *SparkServer) AggregateNodes(ctx context.Context, req *pb.AggregateNodesRequest) (*pb.AggregateNodesResponse, error) {
	aggregateHandler := handler.NewAggregateHandler(s.config)
	return wrapWithGRPCError(aggregateHandler.AggregateNodes(ctx, req))
}

// StorePreimageShare stores the preimage share for the given payment hash.
func (s *SparkServer) StorePreimageShare(ctx context.Context, req *pb.StorePreimageShareRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config, s.onchainHelper)
	return wrapWithGRPCError(emptyResponse, lightningHandler.StorePreimageShare(ctx, req))
}

// GetSigningCommitments gets the signing commitments for the given node ids.
func (s *SparkServer) GetSigningCommitments(ctx context.Context, req *pb.GetSigningCommitmentsRequest) (*pb.GetSigningCommitmentsResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config, s.onchainHelper)
	return wrapWithGRPCError(lightningHandler.GetSigningCommitments(ctx, req))
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (s *SparkServer) InitiatePreimageSwap(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config, s.onchainHelper)
	return wrapWithGRPCError(lightningHandler.InitiatePreimageSwap(ctx, req))
}

// CooperativeExit asks for signatures for refund transactions spending leaves
// and connector outputs on another user's L1 transaction.
func (s *SparkServer) CooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	coopExitHandler := handler.NewCooperativeExitHandler(s.onchainHelper, s.config)
	return wrapWithGRPCError(coopExitHandler.CooperativeExit(ctx, req))
}

// StartSendTransfer initiates a transfer from sender.
func (s *SparkServer) LeafSwap(ctx context.Context, req *pb.LeafSwapRequest) (*pb.LeafSwapResponse, error) {
	transferHander := handler.NewTransferHandler(s.onchainHelper, s.config)
	return transferHander.InitiateLeafSwap(ctx, req)
}

// PrepareTreeAddress prepares the tree address for the given public key.
func (s *SparkServer) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	treeHandler := handler.NewTreeCreationHandler(s.config, s.onchainHelper)
	result, err := wrapWithGRPCError(treeHandler.PrepareTreeAddress(ctx, req))
	if err != nil {
		log.Printf("failed to prepare tree address: %v", err)
	}
	return result, err
}

// CreateTree creates a tree from user input and signs the transactions in the tree.
func (s *SparkServer) CreateTree(ctx context.Context, req *pb.CreateTreeRequest) (*pb.CreateTreeResponse, error) {
	treeHandler := handler.NewTreeCreationHandler(s.config, s.onchainHelper)
	result, err := wrapWithGRPCError(treeHandler.CreateTree(ctx, req))
	if err != nil {
		log.Printf("failed to create tree: %v", err)
	}
	return result, err
}

// GetSigningOperatorList gets the list of signing operators.
func (s *SparkServer) GetSigningOperatorList(ctx context.Context, req *emptypb.Empty) (*pb.GetSigningOperatorListResponse, error) {
	return &pb.GetSigningOperatorListResponse{SigningOperators: s.config.GetSigningOperatorList()}, nil
}
