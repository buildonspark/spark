package grpc

import (
	"context"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/handler"
	"google.golang.org/protobuf/types/known/emptypb"
)

// SparkServer is the grpc server for the Spark protocol.
// It will be used by the user or Spark service provider.
type SparkServer struct {
	pb.UnimplementedSparkServiceServer
	config *so.Config
	db     *ent.Client
}

var emptyResponse = &emptypb.Empty{}

// NewSparkServer creates a new SparkServer.
func NewSparkServer(config *so.Config, db *ent.Client) *SparkServer {
	return &SparkServer{config: config, db: db}
}

// GenerateDepositAddress generates a deposit address for the given public key.
func (s *SparkServer) GenerateDepositAddress(ctx context.Context, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	depositHandler := handler.NewDepositHandler(s.config, s.db)
	return wrapWithGRPCError(depositHandler.GenerateDepositAddress(ctx, s.config, req))
}

// StartDepositTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (s *SparkServer) StartDepositTreeCreation(ctx context.Context, req *pb.StartDepositTreeCreationRequest) (*pb.StartDepositTreeCreationResponse, error) {
	depositHandler := handler.NewDepositHandler(s.config, s.db)
	return wrapWithGRPCError(depositHandler.StartDepositTreeCreation(ctx, s.config, req))
}

// This is deprecated, please use StartDepsitTreeCreation instead.
func (s *SparkServer) StartTreeCreation(ctx context.Context, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	depositHandler := handler.NewDepositHandler(s.config, s.db)
	return wrapWithGRPCError(depositHandler.StartTreeCreation(ctx, s.config, req))
}

// FinalizeNodeSignatures verifies the node signatures and updates the node.
func (s *SparkServer) FinalizeNodeSignatures(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	finalizeSignatureHandler := handler.NewFinalizeSignatureHandler(s.config)
	return wrapWithGRPCError(finalizeSignatureHandler.FinalizeNodeSignatures(ctx, req))
}

// StartSendTransfer initiates a transfer from sender.
func (s *SparkServer) StartSendTransfer(ctx context.Context, req *pb.StartSendTransferRequest) (*pb.StartSendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.StartSendTransfer(ctx, req))
}

// CompleteSendTransfer completes a transfer from sender.
func (s *SparkServer) CompleteSendTransfer(ctx context.Context, req *pb.CompleteSendTransferRequest) (*pb.CompleteSendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.CompleteSendTransfer(ctx, req))
}

// CancelSendTransfer cancels a transfer from sender before key is tweaked.
func (s *SparkServer) CancelSendTransfer(ctx context.Context, req *pb.CancelSendTransferRequest) (*pb.CancelSendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.CancelSendTransfer(ctx, req, handler.CancelSendTransferIntentExternal))
}

// QueryPendingTransfers queries the pending transfers to claim.
func (s *SparkServer) QueryPendingTransfers(ctx context.Context, req *pb.QueryPendingTransfersRequest) (*pb.QueryPendingTransfersResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.QueryPendingTransfers(ctx, req))
}

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (s *SparkServer) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) (*emptypb.Empty, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(emptyResponse, transferHander.ClaimTransferTweakKeys(ctx, req))
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (s *SparkServer) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.ClaimTransferSignRefunds(ctx, req))
}

// AggregateNodes aggregates the given nodes.
func (s *SparkServer) AggregateNodes(ctx context.Context, req *pb.AggregateNodesRequest) (*pb.AggregateNodesResponse, error) {
	aggregateHandler := handler.NewAggregateHandler(s.config)
	return wrapWithGRPCError(aggregateHandler.AggregateNodes(ctx, req))
}

// StorePreimageShare stores the preimage share for the given payment hash.
func (s *SparkServer) StorePreimageShare(ctx context.Context, req *pb.StorePreimageShareRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return wrapWithGRPCError(emptyResponse, lightningHandler.StorePreimageShare(ctx, req))
}

// GetSigningCommitments gets the signing commitments for the given node ids.
func (s *SparkServer) GetSigningCommitments(ctx context.Context, req *pb.GetSigningCommitmentsRequest) (*pb.GetSigningCommitmentsResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return wrapWithGRPCError(lightningHandler.GetSigningCommitments(ctx, req))
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (s *SparkServer) InitiatePreimageSwap(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return wrapWithGRPCError(lightningHandler.InitiatePreimageSwap(ctx, req))
}

// CooperativeExit asks for signatures for refund transactions spending leaves
// and connector outputs on another user's L1 transaction.
func (s *SparkServer) CooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	coopExitHandler := handler.NewCooperativeExitHandler(s.config)
	return wrapWithGRPCError(coopExitHandler.CooperativeExit(ctx, req))
}

// StartLeafSwap initiates a swap of leaves between two users.
func (s *SparkServer) StartLeafSwap(ctx context.Context, req *pb.StartSendTransferRequest) (*pb.StartSendTransferResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.StartLeafSwap(ctx, req))
}

// LeafSwap starts the reverse side of a swap of leaves between two users.
// This is deprecated but remains for backwards compatibility,
// CounterLeafSwap should be used instead.
func (s *SparkServer) LeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.CounterLeafSwap(ctx, req))
}

// CounterLeafSwap starts the reverse side of a swap of leaves between two users.
func (s *SparkServer) CounterLeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.CounterLeafSwap(ctx, req))
}

// RefreshTimelock refreshes the timelocks of a leaf and its ancestors.
func (s *SparkServer) RefreshTimelock(ctx context.Context, req *pb.RefreshTimelockRequest) (*pb.RefreshTimelockResponse, error) {
	handler := handler.NewRefreshTimelockHandler(s.config)
	return wrapWithGRPCError(handler.RefreshTimelock(ctx, req))
}

func (s *SparkServer) ExtendLeaf(ctx context.Context, req *pb.ExtendLeafRequest) (*pb.ExtendLeafResponse, error) {
	handler := handler.NewExtendLeafHandler(s.config)
	return wrapWithGRPCError(handler.ExtendLeaf(ctx, req))
}

// PrepareTreeAddress prepares the tree address for the given public key.
func (s *SparkServer) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	treeHandler := handler.NewTreeCreationHandler(s.config, s.db)
	return wrapWithGRPCError(treeHandler.PrepareTreeAddress(ctx, req))
}

// CreateTree creates a tree from user input and signs the transactions in the tree.
func (s *SparkServer) CreateTree(ctx context.Context, req *pb.CreateTreeRequest) (*pb.CreateTreeResponse, error) {
	treeHandler := handler.NewTreeCreationHandler(s.config, s.db)
	return wrapWithGRPCError(treeHandler.CreateTree(ctx, req))
}

// GetSigningOperatorList gets the list of signing operators.
func (s *SparkServer) GetSigningOperatorList(_ context.Context, _ *emptypb.Empty) (*pb.GetSigningOperatorListResponse, error) {
	return &pb.GetSigningOperatorListResponse{SigningOperators: s.config.GetSigningOperatorList()}, nil
}

func (s *SparkServer) QueryUserSignedRefunds(ctx context.Context, req *pb.QueryUserSignedRefundsRequest) (*pb.QueryUserSignedRefundsResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return wrapWithGRPCError(lightningHandler.QueryUserSignedRefunds(ctx, req))
}

func (s *SparkServer) ProvidePreimage(ctx context.Context, req *pb.ProvidePreimageRequest) (*pb.ProvidePreimageResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return wrapWithGRPCError(lightningHandler.ProvidePreimage(ctx, req))
}

func (s *SparkServer) ReturnLightningPayment(ctx context.Context, req *pb.ReturnLightningPaymentRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return wrapWithGRPCError(lightningHandler.ReturnLightningPayment(ctx, req, false))
}

// StartTokenTransaction reserves revocation keyshares, and fills the revocation commitment (and other SO-derived fields) to create the final token transaction.
func (s *SparkServer) StartTokenTransaction(ctx context.Context, req *pb.StartTokenTransactionRequest) (*pb.StartTokenTransactionResponse, error) {
	tokenTransactionHandler := handler.NewTokenTransactionHandler(s.config, s.db)
	return wrapWithGRPCError(tokenTransactionHandler.StartTokenTransaction(ctx, s.config, req))
}

// QueryNodes queries the details of nodes given either the owner identity public key or a list of node ids.
func (s *SparkServer) QueryNodes(ctx context.Context, req *pb.QueryNodesRequest) (*pb.QueryNodesResponse, error) {
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return wrapWithGRPCError(treeQueryHandler.QueryNodes(ctx, req))
}

// GetTokenTransactionRevocationKeyshares allows the wallet to retrieve the revocation keyshares from each individual SO to
// allow the wallet to combine these shares into the fully resolved revocation secret necessary for transaction finalization.
func (s *SparkServer) SignTokenTransaction(ctx context.Context, req *pb.SignTokenTransactionRequest) (*pb.SignTokenTransactionResponse, error) {
	tokenTransactionHandler := handler.NewTokenTransactionHandler(s.config, s.db)
	return wrapWithGRPCError(tokenTransactionHandler.SignTokenTransaction(ctx, s.config, req))
}

// FinalizeTokenTransaction verifies the revocation secrets constructed by the wallet and passes these keys to the LRC20 Node
// to finalize the transaction. This operation irreversibly spends the inputs associated with the transaction.
func (s *SparkServer) FinalizeTokenTransaction(ctx context.Context, req *pb.FinalizeTokenTransactionRequest) (*emptypb.Empty, error) {
	tokenTransactionHandler := handler.NewTokenTransactionHandler(s.config, s.db)
	return wrapWithGRPCError(tokenTransactionHandler.FinalizeTokenTransaction(ctx, s.config, req))
}

// FreezeTokens prevents transfer of all outputs owned now and in the future by the provided owner public key.
// Unfreeze undos this operation and re-enables transfers.
func (s *SparkServer) FreezeTokens(ctx context.Context, req *pb.FreezeTokensRequest) (*pb.FreezeTokensResponse, error) {
	tokenTransactionHandler := handler.NewTokenTransactionHandler(s.config, s.db)
	return wrapWithGRPCError(tokenTransactionHandler.FreezeTokens(ctx, s.config, req))
}

// QueryTokenTransactions returns the token transactions currently owned by the provided owner public key.
func (s *SparkServer) QueryTokenTransactions(ctx context.Context, req *pb.QueryTokenTransactionsRequest) (*pb.QueryTokenTransactionsResponse, error) {
	tokenTransactionHandler := handler.NewTokenTransactionHandler(s.config, s.db)
	return wrapWithGRPCError(tokenTransactionHandler.QueryTokenTransactions(ctx, s.config, req))
}

// QueryTokenOutputs returns the token outputs currently owned by the provided owner public key.
func (s *SparkServer) QueryTokenOutputs(ctx context.Context, req *pb.QueryTokenOutputsRequest) (*pb.QueryTokenOutputsResponse, error) {
	tokenTransactionHandler := handler.NewTokenTransactionHandler(s.config, s.db)
	return wrapWithGRPCError(tokenTransactionHandler.QueryTokenOutputs(ctx, req))
}

func (s *SparkServer) QueryAllTransfers(ctx context.Context, req *pb.QueryAllTransfersRequest) (*pb.QueryAllTransfersResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return wrapWithGRPCError(transferHander.QueryAllTransfers(ctx, req))
}

func (s *SparkServer) QueryUnusedDepositAddresses(ctx context.Context, req *pb.QueryUnusedDepositAddressesRequest) (*pb.QueryUnusedDepositAddressesResponse, error) {
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return wrapWithGRPCError(treeQueryHandler.QueryUnusedDepositAddresses(ctx, req))
}

func (s *SparkServer) QueryBalance(ctx context.Context, req *pb.QueryBalanceRequest) (*pb.QueryBalanceResponse, error) {
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return wrapWithGRPCError(treeQueryHandler.QueryBalance(ctx, req))
}

// CancelSignedTokenTransaction cancels a token transaction that has been signed but not yet finalized,
// if fewer than the required threshold of operators have signed it.
func (s *SparkServer) CancelSignedTokenTransaction(ctx context.Context, req *pb.CancelSignedTokenTransactionRequest) (*emptypb.Empty, error) {
	tokenTransactionHandler := handler.NewTokenTransactionHandler(s.config, s.db)
	_, err := tokenTransactionHandler.CancelSignedTokenTransaction(ctx, s.config, req)
	return wrapWithGRPCError(emptyResponse, err)
}
