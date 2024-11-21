package grpc

import (
	"context"
	"log"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent_utils"
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
