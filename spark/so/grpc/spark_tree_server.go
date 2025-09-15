package grpc

import (
	"context"

	pb "github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/tree"
	"go.uber.org/zap"
)

// SparkTreeServer is the grpc server for the Spark protocol.
// It will be used by the user or Spark service provider.
type SparkTreeServer struct {
	pb.UnimplementedSparkTreeServiceServer
	config *so.Config
	scorer tree.Scorer
}

// NewSparkTreeServer creates a new SparkTreeServer.
func NewSparkTreeServer(config *so.Config, logger *zap.Logger, dbClient *ent.Client) *SparkTreeServer {
	scorer := tree.NewPolarityScorer(logger, dbClient)
	go scorer.Start(context.Background())
	return &SparkTreeServer{config: config, scorer: scorer}
}

// GetLeafDenominationCounts returns the number of leaves for each denomination.
func (*SparkTreeServer) GetLeafDenominationCounts(ctx context.Context, req *pb.GetLeafDenominationCountsRequest) (*pb.GetLeafDenominationCountsResponse, error) {
	return tree.GetLeafDenominationCounts(ctx, req)
}

// FetchPolarityScores fetches the polarity scores for a given SSP.
func (s *SparkTreeServer) FetchPolarityScores(req *pb.FetchPolarityScoreRequest, stream pb.SparkTreeService_FetchPolarityScoresServer) error {
	return s.scorer.FetchPolarityScores(req, stream)
}
