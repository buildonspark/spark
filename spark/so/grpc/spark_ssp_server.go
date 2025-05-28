package grpc

import (
	"context"

	pb "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler"
)

type SparkSspServer struct {
	pb.UnimplementedSparkSspInternalServiceServer
	config *so.Config
}

func NewSparkSspServer(config *so.Config) *SparkSspServer {
	return &SparkSspServer{config: config}
}

func (s *SparkSspServer) QueryLostNodes(ctx context.Context, req *pb.QueryLostNodesRequest) (*pb.QueryLostNodesResponse, error) {
	sspRequestHandler := handler.NewSspRequestHandler(s.config)
	return errors.WrapWithGRPCError(sspRequestHandler.QueryLostNodes(ctx, req))
}
