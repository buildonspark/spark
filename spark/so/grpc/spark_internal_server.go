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

	_, err = common.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshareID).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetAddress(req.Address).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to link keyshare to deposit address: %v", err)
		return nil, err
	}

	log.Printf("Marked keyshare for deposit address")
	return &emptypb.Empty{}, nil
}

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

	keyPackages, err := ent_utils.GetKeyPackages(ctx, s.config, uuids)
	if err != nil {
		log.Printf("Failed to get key packages: %v", err)
		return nil, err
	}
	keyPackagesArray := make([]*pb.KeyPackage, 0)
	for _, keyPackage := range keyPackages {
		keyPackagesArray = append(keyPackagesArray, keyPackage)
	}

	frostConn, err := common.NewGRPCConnection(s.config.SignerAddress)
	if err != nil {
		log.Printf("Failed to connect to frost: %v", err)
		return nil, err
	}
	defer frostConn.Close()

	frostClient := pb.NewFrostServiceClient(frostConn)
	round1Response, err := frostClient.FrostNonce(ctx, &pb.FrostNonceRequest{
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

		err = ent_utils.StoreSigningNonce(ctx, s.config, nonce, commitment)
		if err != nil {
			log.Printf("Failed to store signing nonce: %v", err)
			return nil, err
		}
	}

	commitments := make([]*pb.SigningCommitment, len(round1Response.Results))
	for i, result := range round1Response.Results {
		commitments[i] = result.Commitments
	}

	return &pb.FrostRound1Response{
		SigningCommitments: commitments,
	}, nil
}

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

	keyPackages, err := ent_utils.GetKeyPackages(ctx, s.config, uuids)
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
	nonces, err := ent_utils.GetSigningNonces(ctx, s.config, commitments)
	if err != nil {
		log.Printf("Failed to get signing nonces: %v", err)
		return nil, err
	}

	signingJobProtos := make([]*pb.FrostSigningJob, 0)

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
		signingJobProto := &pb.FrostSigningJob{
			JobId:           job.JobId,
			Message:         job.Message,
			KeyPackage:      keyPackages[keyshareID],
			VerifyingKey:    job.VerifyingKey,
			Nonce:           nonceProto,
			Commitments:     job.Commitments,
			UserCommitments: job.UserCommitments,
		}
		signingJobProtos = append(signingJobProtos, signingJobProto)
	}

	frostConn, err := common.NewGRPCConnection(s.config.SignerAddress)
	if err != nil {
		log.Printf("Failed to connect to frost: %v", err)
		return nil, err
	}
	defer frostConn.Close()
	frostClient := pb.NewFrostServiceClient(frostConn)

	round2Request := &pb.SignFrostRequest{
		SigningJobs: signingJobProtos,
		Role:        pb.SigningRole_STATECHAIN,
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
