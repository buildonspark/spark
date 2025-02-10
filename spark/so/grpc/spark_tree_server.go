package grpc

import (
	"context"

	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	tree "github.com/lightsparkdev/spark-go/so/tree"
)

// SparkTreeServer is the grpc server for the Spark protocol.
// It will be used by the user or Spark service provider.
type SparkTreeServer struct {
	pb.UnimplementedSparkTreeServiceServer
	config *so.Config
	scorer tree.Scorer
}

// NewSparkTreeServer creates a new SparkTreeServer.
func NewSparkTreeServer(config *so.Config, dbClient *ent.Client) *SparkTreeServer {
	scorer := tree.NewPolarityScorer(dbClient)
	go scorer.Start()
	return &SparkTreeServer{config: config, scorer: scorer}
}

// GetLeafDenominationCounts returns the number of leaves for each denomination.
func (*SparkTreeServer) GetLeafDenominationCounts(ctx context.Context, req *pb.GetLeafDenominationCountsRequest) (*pb.GetLeafDenominationCountsResponse, error) {
	return tree.GetLeafDenominationCounts(ctx, req)
}

// FindLeavesToGiveUser returns the leaves that the SSP should give to the user.
func (s *SparkTreeServer) FindLeavesToGiveUser(ctx context.Context, req *pb.FindLeavesToGiveUserRequest) (*pb.FindLeavesToGiveUserResponse, error) {
	return tree.FindLeavesToGiveUser(ctx, req)
}

// FindLeavesToTakeFromUser returns the leaves that the SSP should receive from the user.
func (s *SparkTreeServer) FindLeavesToTakeFromUser(ctx context.Context, req *pb.FindLeavesToTakeFromUserRequest) (*pb.FindLeavesToTakeFromUserResponse, error) {
	return tree.FindLeavesToTakeFromUser(ctx, req)
}

// ProposeTreeDenominations proposes the denominations for a new tree.
func (*SparkTreeServer) ProposeTreeDenominations(ctx context.Context, req *pb.ProposeTreeDenominationsRequest) (*pb.ProposeTreeDenominationsResponse, error) {
	return tree.ProposeTreeDenominations(ctx, req)
}

// FetchPolarityScores fetches the polarity scores for a given SSP.
func (s *SparkTreeServer) FetchPolarityScores(_ context.Context, _ *pb.FetchPolarityScore) (*pb.FetchPolarityScoreResponse, error) {
	return s.scorer.FetchPolarityScores()
}
