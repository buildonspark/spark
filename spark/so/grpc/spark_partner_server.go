package grpc

import (
	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
)

// SparkPartnerServer implements the partner-facing SparkPartnerService gRPC API.
type SparkPartnerServer struct {
	pbpartner.UnimplementedSparkPartnerServiceServer
}

func NewSparkPartnerServer() *SparkPartnerServer {
	return &SparkPartnerServer{}
}
