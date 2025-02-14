package grpc

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pbspark "github.com/lightsparkdev/spark-go/proto/spark"
	pb "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/handler"
	"github.com/lightsparkdev/spark-go/so/objects"
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
	log.Printf("Marking keyshares as used: %v", req.KeyshareId)
	ids := make([]uuid.UUID, len(req.KeyshareId))
	for i, id := range req.KeyshareId {
		uuid, err := uuid.Parse(id)
		if err != nil {
			log.Printf("Failed to parse keyshare ID: %v", err)
			return nil, err
		}
		ids[i] = uuid
	}
	_, err := ent.MarkSigningKeysharesAsUsed(ctx, s.config, ids)
	if err != nil {
		log.Printf("Failed to mark keyshares as used: %v", err)
		return nil, err
	}

	log.Printf("Marked keyshares as used")

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
			log.Printf("Failed to parse keyshare ID: %v", err)
			return nil, err
		}
		uuids[i] = uuid
	}

	keyPackages, err := ent.GetKeyPackages(ctx, s.config, uuids)
	if err != nil {
		log.Printf("Failed to get key packages: %v", err)
		return nil, err
	}
	keyPackagesArray := make([]*pbfrost.KeyPackage, 0)
	for _, uuid := range uuids {
		keyPackagesArray = append(keyPackagesArray, keyPackages[uuid])
	}

	frostConn, err := common.NewGRPCConnectionWithoutTLS(s.config.SignerAddress)
	if err != nil {
		log.Printf("Failed to connect to frost: %v", err)
		return nil, err
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	round1Response, err := frostClient.FrostNonce(ctx, &pbfrost.FrostNonceRequest{
		KeyPackages: keyPackagesArray,
	})
	if err != nil {
		log.Printf("Failed to send frost round 1: %v", err)
		return nil, err
	}

	for _, result := range round1Response.Results {
		nonce := objects.SigningNonce{}
		err = nonce.UnmarshalProto(result.Nonces)
		if err != nil {
			log.Printf("Failed to unmarshal nonce: %v", err)
			return nil, err
		}
		commitment := objects.SigningCommitment{}
		err = commitment.UnmarshalProto(result.Commitments)
		if err != nil {
			log.Printf("Failed to unmarshal commitment: %v", err)
			return nil, err
		}

		err = ent.StoreSigningNonce(ctx, s.config, nonce, commitment)
		if err != nil {
			log.Printf("Failed to store signing nonce: %v", err)
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
	log.Printf("Round2 request received for operator: %s", req)

	// Fetch key packages in one call.
	uuids := make([]uuid.UUID, len(req.SigningJobs))
	for i, job := range req.SigningJobs {
		uuid, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			log.Printf("Failed to parse keyshare ID: %v", err)
			return nil, err
		}
		uuids[i] = uuid
	}

	keyPackages, err := ent.GetKeyPackages(ctx, s.config, uuids)
	if err != nil {
		log.Printf("Failed to get key packages: %v", err)
		return nil, err
	}

	// Fetch nonces in one call.
	commitments := make([]objects.SigningCommitment, len(req.SigningJobs))
	for i, job := range req.SigningJobs {
		commitments[i] = objects.SigningCommitment{}
		err = commitments[i].UnmarshalProto(job.Commitments[s.config.Identifier])
		if err != nil {
			log.Printf("Failed to unmarshal commitment: %v", err)
			return nil, err
		}
	}
	nonces, err := ent.GetSigningNonces(ctx, s.config, commitments)
	if err != nil {
		log.Printf("Failed to get signing nonces: %v", err)
		return nil, err
	}

	signingJobProtos := make([]*pbfrost.FrostSigningJob, 0)

	for _, job := range req.SigningJobs {
		keyshareID, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			log.Printf("Failed to parse keyshare ID: %v", err)
			return nil, err
		}
		commitment := objects.SigningCommitment{}
		err = commitment.UnmarshalProto(job.Commitments[s.config.Identifier])
		if err != nil {
			log.Printf("Failed to unmarshal commitment: %v", err)
			return nil, err
		}
		nonceProto, err := nonces[commitment.Key()].MarshalProto()
		if err != nil {
			log.Printf("Failed to marshal nonce: %v", err)
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

	frostConn, err := common.NewGRPCConnectionWithoutTLS(s.config.SignerAddress)
	if err != nil {
		log.Printf("Failed to connect to frost: %v", err)
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
		log.Printf("Failed to send frost round 2: %v", err)
		return nil, err
	}

	return &pb.FrostRound2Response{
		Results: round2Response.Results,
	}, nil
}

// PrepareSplitKeyshares prepares the keyshares for a split.
func (s *SparkInternalServer) PrepareSplitKeyshares(ctx context.Context, req *pb.PrepareSplitKeysharesRequest) (*emptypb.Empty, error) {
	splitHandler := handler.NewInternalSplitHandler(s.config)
	return splitHandler.PrepareSplitKeyshares(ctx, req)
}

// FinalizeTreeCreation syncs final tree creation.
func (s *SparkInternalServer) FinalizeTreeCreation(ctx context.Context, req *pb.FinalizeTreeCreationRequest) (*emptypb.Empty, error) {
	depositHandler := handler.NewInternalDepositHandler(s.config)
	err := depositHandler.FinalizeTreeCreation(ctx, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// AggregateNodes aggregates the given nodes.
func (s *SparkInternalServer) AggregateNodes(ctx context.Context, req *pbspark.AggregateNodesRequest) (*emptypb.Empty, error) {
	aggregateHandler := handler.NewAggregateHandler(s.config)
	return aggregateHandler.InternalAggregateNodes(ctx, req)
}

// FinalizeNodesAggregation finalizes nodes aggregation.
func (s *SparkInternalServer) FinalizeNodesAggregation(ctx context.Context, req *pb.FinalizeNodesAggregationRequest) (*emptypb.Empty, error) {
	aggregateHandler := handler.NewAggregateHandler(s.config)
	err := aggregateHandler.InternalFinalizeNodesAggregation(ctx, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// FinalizeTransfer finalizes a transfer
func (s *SparkInternalServer) FinalizeTransfer(ctx context.Context, req *pb.FinalizeTransferRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	err := transferHandler.FinalizeTransfer(ctx, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (s *SparkInternalServer) InitiatePreimageSwap(ctx context.Context, req *pbspark.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	preimageShare, err := lightningHandler.GetPreimageShare(ctx, req)
	if err != nil {
		return nil, err
	}
	return &pb.InitiatePreimageSwapResponse{PreimageShare: preimageShare}, nil
}

// UpdatePreimageRequest updates the preimage request.
func (s *SparkInternalServer) UpdatePreimageRequest(ctx context.Context, req *pb.UpdatePreimageRequestRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	err := lightningHandler.UpdatePreimageRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// PrepareTreeAddress prepares the tree address.
func (s *SparkInternalServer) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	treeCreationHandler := handler.NewInternalTreeCreationHandler(s.config)
	result, err := treeCreationHandler.PrepareTreeAddress(ctx, req)
	if err != nil {
		log.Printf("failed to prepare tree address: %v", err)
	}
	return result, err
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (s *SparkInternalServer) InitiateTransfer(ctx context.Context, req *pb.InitiateTransferRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	err := transferHandler.InitiateTransfer(ctx, req)
	if err != nil {
		log.Printf("failed to initiate transfer: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// InitiateCooperativeExit initiates a cooperative exit.
func (s *SparkInternalServer) InitiateCooperativeExit(ctx context.Context, req *pb.InitiateCooperativeExitRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewInternalTransferHandler(s.config)
	err := transferHandler.InitiateCooperativeExit(ctx, req)
	if err != nil {
		log.Printf("failed to initiate cooperative exit: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ProvidePreimage provides the preimage for the given payment hash.
func (s *SparkInternalServer) ProvidePreimage(ctx context.Context, req *pbspark.ProvidePreimageRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	_, err := lightningHandler.ProvidePreimageInternal(ctx, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *SparkInternalServer) ReturnLightningPayment(ctx context.Context, req *pbspark.ReturnLightningPaymentRequest) (*emptypb.Empty, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return wrapWithGRPCError(lightningHandler.ReturnLightningPayment(ctx, req, true))
}

// StartTokenTransactionInternal validates a token transaction and saves it to the database.
func (s *SparkInternalServer) StartTokenTransactionInternal(ctx context.Context, req *pb.StartTokenTransactionInternalRequest) (*emptypb.Empty, error) {
	tokenTransactionHandler := handler.NewInternalTokenTransactionHandler(s.config)
	_, err := tokenTransactionHandler.StartTokenTransactionInternal(ctx, s.config, req)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
