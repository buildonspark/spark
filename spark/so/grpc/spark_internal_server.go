package grpc

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/so/protoconverter"

	"github.com/lightsparkdev/spark/so/handler/tokens"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"google.golang.org/protobuf/types/known/emptypb"
)

// SparkInternalServer is the grpc server for internal spark services.
// This server is only used by the operator.
type SparkInternalServer struct {
	pb.UnimplementedSparkInternalServiceServer
	config *so.Config
}

// NewSparkInternalServer creates a new SparkInternalServer.
func NewSparkInternalServer(config *so.Config) *SparkInternalServer {
	return &SparkInternalServer{config: config}
}

// MarkKeysharesAsUsed marks the keyshares as used.
// It will return an error if the key is not found or the key is already used.
func (s *SparkInternalServer) MarkKeysharesAsUsed(ctx context.Context, req *pb.MarkKeysharesAsUsedRequest) (*emptypb.Empty, error) {
	ids := make([]uuid.UUID, len(req.KeyshareId))
	for i, id := range req.KeyshareId {
		keyshareID, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		ids[i] = keyshareID
	}
	_, err := ent.MarkSigningKeysharesAsUsed(ctx, s.config, ids)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// MarkKeyshareForDepositAddress links the keyshare to a deposit address.
func (s *SparkInternalServer) MarkKeyshareForDepositAddress(ctx context.Context, req *pb.MarkKeyshareForDepositAddressRequest) (*pb.MarkKeyshareForDepositAddressResponse, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return depositHandler.MarkKeyshareForDepositAddress(ctx, req)
}

func (s *SparkInternalServer) GenerateStaticDepositAddressProofs(ctx context.Context, req *pb.GenerateStaticDepositAddressProofsRequest) (*pb.GenerateStaticDepositAddressProofsResponse, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return depositHandler.GenerateStaticDepositAddressProofs(ctx, req)
}

func (s *SparkInternalServer) ReserveEntityDkgKey(ctx context.Context, req *pb.ReserveEntityDkgKeyRequest) (*emptypb.Empty, error) {
	entityDkgKeyHandler := handler.NewEntityDkgKeyHandler(s.config)
	if err := entityDkgKeyHandler.ReserveEntityDkgKey(ctx, req); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// FrostRound1 handles the FROST nonce generation.
func (s *SparkInternalServer) FrostRound1(ctx context.Context, req *pb.FrostRound1Request) (*pb.FrostRound1Response, error) {
	frostSigningHandler := signing_handler.NewFrostSigningHandler(s.config)
	return frostSigningHandler.FrostRound1(ctx, req)
}

// FrostRound2 handles FROST signing.
func (s *SparkInternalServer) FrostRound2(ctx context.Context, req *pb.FrostRound2Request) (*pb.FrostRound2Response, error) {
	frostSigningHandler := signing_handler.NewFrostSigningHandler(s.config)
	return frostSigningHandler.FrostRound2(ctx, req)
}

// FinalizeTreeCreation syncs final tree creation.
func (s *SparkInternalServer) FinalizeTreeCreation(ctx context.Context, req *pb.FinalizeTreeCreationRequest) (*emptypb.Empty, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return &emptypb.Empty{}, depositHandler.FinalizeTreeCreation(ctx, req)
}

// FinalizeTransfer finalizes a transfer
func (s *SparkInternalServer) FinalizeTransfer(ctx context.Context, req *pb.FinalizeTransferRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return &emptypb.Empty{}, transferHandler.FinalizeTransfer(ctx, req)
}

// FinalizeRefreshTimelock finalizes the refresh timelock.
func (s *SparkInternalServer) FinalizeRefreshTimelock(ctx context.Context, req *pb.FinalizeRefreshTimelockRequest) (*emptypb.Empty, error) {
	refreshTimelockHandler := handler.NewInternalRefreshTimelockHandler(s.config)
	return &emptypb.Empty{}, refreshTimelockHandler.FinalizeRefreshTimelock(ctx, req)
}

func (s *SparkInternalServer) FinalizeExtendLeaf(ctx context.Context, req *pb.FinalizeExtendLeafRequest) (*emptypb.Empty, error) {
	extendLeafHandler := handler.NewInternalExtendLeafHandler(s.config)
	return &emptypb.Empty{}, extendLeafHandler.FinalizeExtendLeaf(ctx, req)
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (s *SparkInternalServer) InitiatePreimageSwap(ctx context.Context, req *pbspark.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	preimageShare, err := lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil)
	return &pb.InitiatePreimageSwapResponse{PreimageShare: preimageShare}, err
}

func (s *SparkInternalServer) InitiatePreimageSwapV2(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	preimageShare, err := lightningHandler.GetPreimageShare(ctx, req.Request, req.CpfpRefundSignatures, req.DirectRefundSignatures, req.DirectFromCpfpRefundSignatures)
	return &pb.InitiatePreimageSwapResponse{PreimageShare: preimageShare}, err
}

func (s *SparkInternalServer) FinalizeRenewRefundTimelock(ctx context.Context, req *pb.FinalizeRenewRefundTimelockRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *SparkInternalServer) FinalizeRenewNodeTimelock(ctx context.Context, req *pb.FinalizeRenewNodeTimelockRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// UpdatePreimageRequest updates the preimage request.
func (s *SparkInternalServer) UpdatePreimageRequest(ctx context.Context, req *pb.UpdatePreimageRequestRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return &emptypb.Empty{}, lightningHandler.UpdatePreimageRequest(ctx, req)
}

// PrepareTreeAddress prepares the tree address.
func (s *SparkInternalServer) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	treeCreationHandler := handler.NewInternalTreeCreationHandler(s.config)
	return treeCreationHandler.PrepareTreeAddress(ctx, req)
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (s *SparkInternalServer) InitiateTransfer(ctx context.Context, req *pb.InitiateTransferRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return &emptypb.Empty{}, transferHandler.InitiateTransfer(ctx, req)
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (s *SparkInternalServer) DeliverSenderKeyTweak(ctx context.Context, req *pb.DeliverSenderKeyTweakRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return &emptypb.Empty{}, transferHandler.DeliverSenderKeyTweak(ctx, req)
}

// InitiateCooperativeExit initiates a cooperative exit.
func (s *SparkInternalServer) InitiateCooperativeExit(ctx context.Context, req *pb.InitiateCooperativeExitRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return &emptypb.Empty{}, transferHandler.InitiateCooperativeExit(ctx, req)
}

// ProvidePreimage provides the preimage for the given payment hash.
func (s *SparkInternalServer) ProvidePreimage(ctx context.Context, req *pb.ProvidePreimageRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	_, err := lightningHandler.ValidatePreimageInternal(ctx, req)
	return &emptypb.Empty{}, err
}

func (s *SparkInternalServer) ReturnLightningPayment(ctx context.Context, req *pbspark.ReturnLightningPaymentRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.ReturnLightningPayment(ctx, req, true)
}

// StartTokenTransactionInternal validates a token transaction and saves it to the database.
func (s *SparkInternalServer) StartTokenTransactionInternal(ctx context.Context, req *pb.StartTokenTransactionInternalRequest) (*emptypb.Empty, error) {
	internalPrepareHandler := tokens.NewInternalPrepareTokenHandler(s.config)
	prepareReq, err := protoconverter.TokenProtoPrepareTransactionRequestFromSpark(req)
	if err != nil {
		return nil, fmt.Errorf("failed to convert request into v1: %w", err)
	}
	_, err = internalPrepareHandler.PrepareTokenTransactionInternal(ctx, prepareReq)

	return &emptypb.Empty{}, err
}

func (s *SparkInternalServer) InitiateSettleReceiverKeyTweak(ctx context.Context, req *pb.InitiateSettleReceiverKeyTweakRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewTransferHandler(s.config)
	return &emptypb.Empty{}, transferHandler.InitiateSettleReceiverKeyTweak(ctx, req)
}

func (s *SparkInternalServer) SettleReceiverKeyTweak(ctx context.Context, req *pb.SettleReceiverKeyTweakRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewTransferHandler(s.config)
	return &emptypb.Empty{}, transferHandler.SettleReceiverKeyTweak(ctx, req)
}

func (s *SparkInternalServer) SettleSenderKeyTweak(ctx context.Context, req *pb.SettleSenderKeyTweakRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return &emptypb.Empty{}, transferHandler.SettleSenderKeyTweak(ctx, req)
}

// Register a utxo swap in all SEs so they can not be called concurrently to spend the same utxo
func (s *SparkInternalServer) CreateUtxoSwap(ctx context.Context, req *pb.CreateUtxoSwapRequest) (*pb.CreateUtxoSwapResponse, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return depositHandler.CreateUtxoSwap(ctx, s.config, req)
}

func (s *SparkInternalServer) CreateStaticDepositUtxoSwap(ctx context.Context, req *pb.CreateStaticDepositUtxoSwapRequest) (*pb.CreateStaticDepositUtxoSwapResponse, error) {
	depositHandler := handler.NewStaticDepositInternalHandler(s.config)
	return depositHandler.CreateStaticDepositUtxoSwap(ctx, s.config, req)
}

func (s *SparkInternalServer) CreateStaticDepositUtxoRefund(ctx context.Context, req *pb.CreateStaticDepositUtxoRefundRequest) (*pb.CreateStaticDepositUtxoRefundResponse, error) {
	depositHandler := handler.NewStaticDepositInternalHandler(s.config)
	return depositHandler.CreateStaticDepositUtxoRefund(ctx, s.config, req)
}

func (s *SparkInternalServer) QueryTokenOutputsInternal(ctx context.Context, req *pbspark.QueryTokenOutputsRequest) (*pbspark.QueryTokenOutputsResponse, error) {
	queryTokenHandler := tokens.NewQueryTokenHandler(s.config)
	return queryTokenHandler.QueryTokenOutputsSpark(ctx, req)
}

// Cancel a utxo swap in an SO after the creation of the swap failed
func (s *SparkInternalServer) RollbackUtxoSwap(ctx context.Context, req *pb.RollbackUtxoSwapRequest) (*pb.RollbackUtxoSwapResponse, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return depositHandler.RollbackUtxoSwap(ctx, s.config, req)
}

// Mark a utxo swap as COMPLETE in all SEs
func (s *SparkInternalServer) UtxoSwapCompleted(ctx context.Context, req *pb.UtxoSwapCompletedRequest) (*pb.UtxoSwapCompletedResponse, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return depositHandler.UtxoSwapCompleted(ctx, s.config, req)
}

func (s *SparkInternalServer) QueryLeafSigningPubkeys(ctx context.Context, req *pb.QueryLeafSigningPubkeysRequest) (*pb.QueryLeafSigningPubkeysResponse, error) {
	investigationHandler := handler.NewInvestigationHandler(s.config)
	return investigationHandler.QueryLeafSigningPubkeys(ctx, req)
}

func (s *SparkInternalServer) ResolveLeafInvestigation(ctx context.Context, req *pb.ResolveLeafInvestigationRequest) (*emptypb.Empty, error) {
	investigationHandler := handler.NewInvestigationHandler(s.config)
	return investigationHandler.ResolveLeafInvestigation(ctx, req)
}

func (s *SparkInternalServer) Gossip(ctx context.Context, req *pbgossip.GossipMessage) (*emptypb.Empty, error) {
	gossipHandler := handler.NewGossipHandler(s.config)
	return &emptypb.Empty{}, gossipHandler.HandleGossipMessage(ctx, req, false)
}

func (s *SparkInternalServer) FixKeyshare(ctx context.Context, req *pb.FixKeyshareRequest) (*emptypb.Empty, error) {
	h := handler.NewFixKeyshareHandler(s.config)
	return &emptypb.Empty{}, h.FixKeyshare(ctx, req)
}

func (s *SparkInternalServer) FixKeyshareRound1(ctx context.Context, req *pb.FixKeyshareRound1Request) (*pb.FixKeyshareRound1Response, error) {
	h := handler.NewFixKeyshareHandler(s.config)
	return h.Round1(ctx, req)
}

func (s *SparkInternalServer) FixKeyshareRound2(ctx context.Context, req *pb.FixKeyshareRound2Request) (*pb.FixKeyshareRound2Response, error) {
	h := handler.NewFixKeyshareHandler(s.config)
	return h.Round2(ctx, req)
}

func (s *SparkInternalServer) GetTransfers(ctx context.Context, req *pb.GetTransfersRequest) (*pb.GetTransfersResponse, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return transferHandler.GetTransfers(ctx, req)
}
