package grpc

import (
	"context"

	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/partner"
)

// SparkPartnerServer implements the partner-facing SparkPartnerService gRPC API.
type SparkPartnerServer struct {
	pbpartner.UnimplementedSparkPartnerServiceServer
	rwClient *partner.RisingWaveClient
}

// NewSparkPartnerServer creates a new SparkPartnerServer.
// rwClient may be nil if RisingWave is not configured.
func NewSparkPartnerServer(rwClient *partner.RisingWaveClient) *SparkPartnerServer {
	return &SparkPartnerServer{rwClient: rwClient}
}

// QuerySparkTransactionVolumes returns aggregated transaction volumes for the authenticated partner.
func (s *SparkPartnerServer) QuerySparkTransactionVolumes(ctx context.Context, req *pbpartner.QuerySparkTransactionVolumesRequest) (*pbpartner.QuerySparkTransactionVolumesResponse, error) {
	partnerQueryHandler := handler.NewPartnerQueryHandler(s.rwClient)
	return partnerQueryHandler.QuerySparkTransactionVolumes(ctx, req)
}
