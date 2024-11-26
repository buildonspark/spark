package grpc

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent_utils"
	"github.com/lightsparkdev/spark-go/so/objects"
	"google.golang.org/protobuf/types/known/emptypb"
)

type SparkInternalServer struct {
	pb.UnimplementedSparkInternalServiceServer
	config *so.Config
}

func NewSparkInternalServer(config *so.Config) *SparkInternalServer {
	return &SparkInternalServer{config: config}
}

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
	err := ent_utils.MarkSigningKeysharesAsUsed(ctx, s.config, ids)
	if err != nil {
		log.Printf("Failed to mark keyshares as used: %v", err)
		return nil, err
	}

	log.Printf("Marked keyshares as used")

	return &emptypb.Empty{}, nil
}

func (s *SparkInternalServer) MarkKeyshareForDepositAddress(ctx context.Context, req *pb.MarkKeyshareForDepositAddressRequest) (*emptypb.Empty, error) {
	log.Printf("Marking keyshare for deposit address: %v", req.KeyshareId)

	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		log.Printf("Failed to parse keyshare ID: %v", err)
		return nil, err
	}

	_, err = common.GetDbFromContext(ctx).DepositAddress.Create().SetSigningKeyshareID(keyshareID).SetAddress(req.Address).Save(ctx)
	if err != nil {
		log.Printf("Failed to link keyshare to deposit address: %v", err)
		return nil, err
	}

	log.Printf("Marked keyshare for deposit address")
	return &emptypb.Empty{}, nil
}

func (s *SparkInternalServer) FrostRound1(ctx context.Context, req *pb.FrostRound1Request) (*pb.FrostRound1Response, error) {
	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		log.Printf("Failed to parse keyshare ID: %v", err)
		return nil, err
	}
	keyPackage, err := ent_utils.GetKeyPackage(ctx, s.config, keyshareID)
	if err != nil {
		log.Printf("Failed to get key package: %v", err)
		return nil, err
	}

	frostConn, err := common.NewGRPCConnection(s.config.SignerAddress)
	if err != nil {
		log.Printf("Failed to connect to frost: %v", err)
		return nil, err
	}
	defer frostConn.Close()

	frostClient := pb.NewFrostServiceClient(frostConn)
	round1Response, err := frostClient.FrostNonce(ctx, &pb.FrostNonceRequest{
		KeyPackage: keyPackage,
	})
	if err != nil {
		log.Printf("Failed to send frost round 1: %v", err)
		return nil, err
	}

	nonce := objects.SigningNonce{}
	err = nonce.UnmarshalProto(round1Response.Nonces)
	if err != nil {
		log.Printf("Failed to unmarshal nonce: %v", err)
		return nil, err
	}
	commitment := objects.SigningCommitment{}
	err = commitment.UnmarshalProto(round1Response.Commitments)
	if err != nil {
		log.Printf("Failed to unmarshal commitment: %v", err)
		return nil, err
	}

	err = ent_utils.StoreSigningNonce(ctx, s.config, nonce, commitment)
	if err != nil {
		log.Printf("Failed to store signing nonce: %v", err)
		return nil, err
	}

	return &pb.FrostRound1Response{
		SigningCommitment: round1Response.Commitments,
	}, nil
}

func (s *SparkInternalServer) FrostRound2(ctx context.Context, req *pb.FrostRound2Request) (*pb.FrostRound2Response, error) {
	log.Printf("Round2 request received for operator: %s", req)
	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		log.Printf("Failed to parse keyshare ID: %v", err)
		return nil, err
	}
	keyPackage, err := ent_utils.GetKeyPackage(ctx, s.config, keyshareID)
	if err != nil {
		log.Printf("Failed to get key package: %v", err)
		return nil, err
	}

	selfCommitment := objects.SigningCommitment{}
	err = selfCommitment.UnmarshalProto(req.Commitments[s.config.Identifier])
	if err != nil {
		log.Printf("Failed to unmarshal self commitment: %v", err)
		return nil, err
	}
	nonce, err := ent_utils.GetSigningNonceFromCommitment(ctx, s.config, selfCommitment)
	if err != nil {
		log.Printf("Failed to get signing nonce from commitment: %v", err)
		return nil, err
	}
	nonceProto, err := nonce.MarshalProto()
	if err != nil {
		log.Printf("Failed to marshal nonce: %v", err)
		return nil, err
	}

	frostConn, err := common.NewGRPCConnection(s.config.SignerAddress)
	if err != nil {
		log.Printf("Failed to connect to frost: %v", err)
		return nil, err
	}
	defer frostConn.Close()
	frostClient := pb.NewFrostServiceClient(frostConn)

	round2Request := &pb.SignFrostRequest{
		Message:         req.Message,
		KeyPackage:      keyPackage,
		VerifyingKey:    req.VerifyingKey,
		Nonce:           nonceProto,
		Commitments:     req.Commitments,
		UserCommitments: req.UserCommitments,
		Role:            pb.SigningRole_STATECHAIN,
	}
	round2Response, err := frostClient.SignFrost(ctx, round2Request)
	if err != nil {
		log.Printf("Failed to send frost round 2: %v", err)
		return nil, err
	}

	return &pb.FrostRound2Response{
		SignatureShare: round2Response.SignatureShare,
	}, nil
}
