package grpc

import (
	"context"

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
	ids := make([]uuid.UUID, len(req.KeyshareId))
	for i, id := range req.KeyshareId {
		uuid, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		ids[i] = uuid
	}
	err := ent_utils.MarkSigningKeysharesAsUsed(ctx, s.config, ids)
	if err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}
