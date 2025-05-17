package grpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/lrc20"
	"github.com/lightsparkdev/spark/so/objects"
	"google.golang.org/protobuf/types/known/emptypb"
)

// SparkInternalServer is the grpc server for internal spark services.
// This server is only used by the operator.
type SparkInternalServer struct {
	pb.UnimplementedSparkInternalServiceServer
	config      *so.Config
	lrc20Client *lrc20.Client
}

// NewSparkInternalServer creates a new SparkInternalServer.
func NewSparkInternalServer(config *so.Config, client *lrc20.Client) *SparkInternalServer {
	return &SparkInternalServer{config: config, lrc20Client: client}
}

// MarkKeysharesAsUsed marks the keyshares as used.
// It will return an error if the key is not found or the key is already used.
func (s *SparkInternalServer) MarkKeysharesAsUsed(ctx context.Context, req *pb.MarkKeysharesAsUsedRequest) (*emptypb.Empty, error) {
	ids := make([]uuid.UUID, len(req.KeyshareId))
	for i, id := range req.KeyshareId {
		uuid, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		ids[i] = uuid
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

// FrostRound1 handles the FROST nonce generation.
func (s *SparkInternalServer) FrostRound1(ctx context.Context, req *pb.FrostRound1Request) (*pb.FrostRound1Response, error) {
	uuids := make([]uuid.UUID, len(req.KeyshareIds))
	for i, id := range req.KeyshareIds {
		uuid, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		uuids[i] = uuid
	}

	keyPackages, err := ent.GetKeyPackages(ctx, s.config, uuids)
	if err != nil {
		return nil, err
	}
	keyPackagesArray := make([]*pbfrost.KeyPackage, 0)
	for _, uuid := range uuids {
		keyPackagesArray = append(keyPackagesArray, keyPackages[uuid])
	}

	frostConn, err := common.NewGRPCConnectionWithoutTLS(s.config.SignerAddress, nil)
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	round1Response, err := frostClient.FrostNonce(ctx, &pbfrost.FrostNonceRequest{
		KeyPackages: keyPackagesArray,
	})
	if err != nil {
		return nil, err
	}

	for _, result := range round1Response.Results {
		nonce := objects.SigningNonce{}
		err = nonce.UnmarshalProto(result.Nonces)
		if err != nil {
			return nil, err
		}
		commitment := objects.SigningCommitment{}
		err = commitment.UnmarshalProto(result.Commitments)
		if err != nil {
			return nil, err
		}

		err = ent.StoreSigningNonce(ctx, s.config, nonce, commitment)
		if err != nil {
			return nil, err
		}
	}

	commitments := make([]*pbcommon.SigningCommitment, len(round1Response.Results))
	for i, result := range round1Response.Results {
		commitments[i] = result.Commitments
	}

	return &pb.FrostRound1Response{
		SigningCommitments: commitments,
	}, nil
}

// FrostRound2 handles FROST signing.
func (s *SparkInternalServer) FrostRound2(ctx context.Context, req *pb.FrostRound2Request) (*pb.FrostRound2Response, error) {
	// Fetch key packages in one call.
	uuids := make([]uuid.UUID, len(req.SigningJobs))
	for i, job := range req.SigningJobs {
		uuid, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			return nil, err
		}
		uuids[i] = uuid
	}

	keyPackages, err := ent.GetKeyPackages(ctx, s.config, uuids)
	if err != nil {
		return nil, err
	}

	// Fetch nonces in one call.
	commitments := make([]objects.SigningCommitment, len(req.SigningJobs))
	for i, job := range req.SigningJobs {
		commitments[i] = objects.SigningCommitment{}
		err = commitments[i].UnmarshalProto(job.Commitments[s.config.Identifier])
		if err != nil {
			return nil, err
		}
	}
	nonces, err := ent.GetSigningNonces(ctx, s.config, commitments)
	if err != nil {
		return nil, err
	}

	signingJobProtos := make([]*pbfrost.FrostSigningJob, 0)

	for _, job := range req.SigningJobs {
		keyshareID, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			return nil, err
		}
		commitment := objects.SigningCommitment{}
		err = commitment.UnmarshalProto(job.Commitments[s.config.Identifier])
		if err != nil {
			return nil, err
		}
		nonceEnt := nonces[commitment.Key()]
		// TODO(zhenlu): Add a test for this (LIG-7596).
		if len(nonceEnt.Message) > 0 {
			if !bytes.Equal(nonceEnt.Message, job.Message) {
				return nil, fmt.Errorf("this signing nonce is already used for a different message %s, cannot use it for this message %s", hex.EncodeToString(nonceEnt.Message), hex.EncodeToString(job.Message))
			}
		} else {
			_, err = nonceEnt.Update().SetMessage(job.Message).Save(ctx)
			if err != nil {
				return nil, err
			}
		}
		nonceObject := objects.SigningNonce{}
		err = nonceObject.UnmarshalBinary(nonceEnt.Nonce)
		if err != nil {
			return nil, err
		}
		nonceProto, err := nonceObject.MarshalProto()
		if err != nil {
			return nil, err
		}
		signingJobProto := &pbfrost.FrostSigningJob{
			JobId:            job.JobId,
			Message:          job.Message,
			KeyPackage:       keyPackages[keyshareID],
			VerifyingKey:     job.VerifyingKey,
			Nonce:            nonceProto,
			Commitments:      job.Commitments,
			UserCommitments:  job.UserCommitments,
			AdaptorPublicKey: job.AdaptorPublicKey,
		}
		signingJobProtos = append(signingJobProtos, signingJobProto)
	}

	frostConn, err := common.NewGRPCConnectionWithoutTLS(s.config.SignerAddress, nil)
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	round2Request := &pbfrost.SignFrostRequest{
		SigningJobs: signingJobProtos,
		Role:        pbfrost.SigningRole_STATECHAIN,
	}
	round2Response, err := frostClient.SignFrost(ctx, round2Request)
	if err != nil {
		return nil, err
	}

	return &pb.FrostRound2Response{
		Results: round2Response.Results,
	}, nil
}

// PrepareSplitKeyshares prepares the keyshares for a split.
func (s *SparkInternalServer) PrepareSplitKeyshares(ctx context.Context, req *pb.PrepareSplitKeysharesRequest) (*emptypb.Empty, error) {
	splitHandler := handler.NewInternalSplitHandler(s.config)
	return errors.WrapWithGRPCError(splitHandler.PrepareSplitKeyshares(ctx, req))
}

// FinalizeTreeCreation syncs final tree creation.
func (s *SparkInternalServer) FinalizeTreeCreation(ctx context.Context, req *pb.FinalizeTreeCreationRequest) (*emptypb.Empty, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, depositHandler.FinalizeTreeCreation(ctx, req))
}

// AggregateNodes aggregates the given nodes.
func (s *SparkInternalServer) AggregateNodes(ctx context.Context, req *pbspark.AggregateNodesRequest) (*emptypb.Empty, error) {
	aggregateHandler := handler.NewAggregateHandler(s.config)
	return errors.WrapWithGRPCError(aggregateHandler.InternalAggregateNodes(ctx, req))
}

// FinalizeNodesAggregation finalizes nodes aggregation.
func (s *SparkInternalServer) FinalizeNodesAggregation(ctx context.Context, req *pb.FinalizeNodesAggregationRequest) (*emptypb.Empty, error) {
	aggregateHandler := handler.NewAggregateHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, aggregateHandler.InternalFinalizeNodesAggregation(ctx, req))
}

// FinalizeTransfer finalizes a transfer
func (s *SparkInternalServer) FinalizeTransfer(ctx context.Context, req *pb.FinalizeTransferRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, transferHandler.FinalizeTransfer(ctx, req))
}

// FinalizeRefreshTimelock finalizes the refresh timelock.
func (s *SparkInternalServer) FinalizeRefreshTimelock(ctx context.Context, req *pb.FinalizeRefreshTimelockRequest) (*emptypb.Empty, error) {
	refreshTimelockHandler := handler.NewInternalRefreshTimelockHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, refreshTimelockHandler.FinalizeRefreshTimelock(ctx, req))
}

func (s *SparkInternalServer) FinalizeExtendLeaf(ctx context.Context, req *pb.FinalizeExtendLeafRequest) (*emptypb.Empty, error) {
	extendLeafHandler := handler.NewInternalExtendLeafHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, extendLeafHandler.FinalizeExtendLeaf(ctx, req))
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (s *SparkInternalServer) InitiatePreimageSwap(ctx context.Context, req *pbspark.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	preimageShare, err := lightningHandler.GetPreimageShare(ctx, req)
	return errors.WrapWithGRPCError(&pb.InitiatePreimageSwapResponse{PreimageShare: preimageShare}, err)
}

// UpdatePreimageRequest updates the preimage request.
func (s *SparkInternalServer) UpdatePreimageRequest(ctx context.Context, req *pb.UpdatePreimageRequestRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, lightningHandler.UpdatePreimageRequest(ctx, req))
}

// PrepareTreeAddress prepares the tree address.
func (s *SparkInternalServer) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	treeCreationHandler := handler.NewInternalTreeCreationHandler(s.config)
	return errors.WrapWithGRPCError(treeCreationHandler.PrepareTreeAddress(ctx, req))
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (s *SparkInternalServer) InitiateTransfer(ctx context.Context, req *pb.InitiateTransferRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, transferHandler.InitiateTransfer(ctx, req))
}

// InitiateCooperativeExit initiates a cooperative exit.
func (s *SparkInternalServer) InitiateCooperativeExit(ctx context.Context, req *pb.InitiateCooperativeExitRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, transferHandler.InitiateCooperativeExit(ctx, req))
}

// ProvidePreimage provides the preimage for the given payment hash.
func (s *SparkInternalServer) ProvidePreimage(ctx context.Context, req *pbspark.ProvidePreimageRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	_, err := lightningHandler.ProvidePreimageInternal(ctx, req)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, err)
}

func (s *SparkInternalServer) ReturnLightningPayment(ctx context.Context, req *pbspark.ReturnLightningPaymentRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return errors.WrapWithGRPCError(lightningHandler.ReturnLightningPayment(ctx, req, true))
}

// StartTokenTransactionInternal validates a token transaction and saves it to the database.
func (s *SparkInternalServer) StartTokenTransactionInternal(ctx context.Context, req *pb.StartTokenTransactionInternalRequest) (*emptypb.Empty, error) {
	tokenTransactionHandler := handler.NewInternalTokenTransactionHandler(s.config, s.lrc20Client)
	return errors.WrapWithGRPCError(tokenTransactionHandler.StartTokenTransactionInternal(ctx, s.config, req))
}

// CancelTransfer cancels a transfer from sender before key is tweaked.
func (s *SparkInternalServer) CancelTransfer(ctx context.Context, req *pbspark.CancelTransferRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	_, err := transferHandler.CancelTransfer(ctx, req, handler.CancelTransferIntentInternal)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, err)
}

func (s *SparkInternalServer) InitiateSettleReceiverKeyTweak(ctx context.Context, req *pb.InitiateSettleReceiverKeyTweakRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewTransferHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, transferHandler.InitiateSettleReceiverKeyTweak(ctx, req))
}

func (s *SparkInternalServer) SettleReceiverKeyTweak(ctx context.Context, req *pb.SettleReceiverKeyTweakRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewTransferHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, transferHandler.SettleReceiverKeyTweak(ctx, req))
}

func (s *SparkInternalServer) SettleSenderKeyTweak(ctx context.Context, req *pb.SettleSenderKeyTweakRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	return errors.WrapWithGRPCError(&emptypb.Empty{}, transferHandler.SettleSenderKeyTweak(ctx, req))
}

// Register a utxo swap in all SEs so they can not be called concurrently to spend the same utxo
func (s *SparkInternalServer) CreateUtxoSwap(ctx context.Context, req *pbspark.InitiateUtxoSwapRequest) (*pb.CreateUtxoSwapResponse, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	return errors.WrapWithGRPCError(depositHandler.CreateUtxoSwap(ctx, s.config, req))
}

func (s *SparkInternalServer) QueryTokenOutputsInternal(ctx context.Context, req *pbspark.QueryTokenOutputsRequest) (*pbspark.QueryTokenOutputsResponse, error) {
	tokenTransactionHandler := handler.NewInternalTokenTransactionHandler(s.config, s.lrc20Client)
	return errors.WrapWithGRPCError(tokenTransactionHandler.QueryTokenOutputsInternal(ctx, req))
}
