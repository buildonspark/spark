//go:build !lightspark

package main

import (
	"fmt"

	"github.com/lightsparkdev/spark/common"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbauthn "github.com/lightsparkdev/spark/proto/spark_authn"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbtoken "github.com/lightsparkdev/spark/proto/spark_token"
	pbtokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	pbtree "github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authninternal"
	"github.com/lightsparkdev/spark/so/dkg"
	"github.com/lightsparkdev/spark/so/ent"
	sparkgrpc "github.com/lightsparkdev/spark/so/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func RegisterGrpcServers(
	grpcServer *grpc.Server,
	args *args,
	config *so.Config,
	dbClient *ent.Client,
	frostClient *grpc.ClientConn,
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier,
	mockAction *common.MockAction,
) error {
	if mockAction != nil {
		mockServer := sparkgrpc.NewMockServer(config, mockAction, dbClient)
		pbmock.RegisterMockServiceServer(grpcServer, mockServer)
	}

	if !args.DisableDKG {
		dkgServer := dkg.NewServer(frostClient, config)
		pbdkg.RegisterDKGServiceServer(grpcServer, dkgServer)
	}

	// Private/Internal SO <-> SO endpoint
	sparkInternalServer := sparkgrpc.NewSparkInternalServer(config)
	pbinternal.RegisterSparkInternalServiceServer(grpcServer, sparkInternalServer)

	// Public SO endpoint
	sparkServer := sparkgrpc.NewSparkServer(config, mockAction)
	pbspark.RegisterSparkServiceServer(grpcServer, sparkServer)

	// Public SO token endpoint
	sparkTokenServer := sparkgrpc.NewSparkTokenServer(config, config, dbClient)
	pbtoken.RegisterSparkTokenServiceServer(grpcServer, sparkTokenServer)

	// Gossip endpoint
	gossipServer := sparkgrpc.NewGossipServer(config)
	pbgossip.RegisterGossipServiceServer(grpcServer, gossipServer)

	// Private/Internal token SO <-> SO endpoint
	sparkTokenInternalServer := sparkgrpc.NewSparkTokenInternalServer(config, dbClient)
	pbtokeninternal.RegisterSparkTokenInternalServiceServer(grpcServer, sparkTokenInternalServer)

	// SSP receive private/internal endpoint
	treeServer := sparkgrpc.NewSparkTreeServer(config, dbClient)
	pbtree.RegisterSparkTreeServiceServer(grpcServer, treeServer)

	// Public ID challenge auth endpoint
	authnServer, err := sparkgrpc.NewAuthnServer(sparkgrpc.AuthnServerConfig{
		IdentityPrivateKey: config.IdentityPrivateKey,
		ChallengeTimeout:   args.ChallengeTimeout,
		SessionDuration:    args.SessionDuration,
	}, sessionTokenCreatorVerifier)
	if err != nil {
		return fmt.Errorf("failed to create authentication server: %w", err)
	}
	pbauthn.RegisterSparkAuthnServiceServer(grpcServer, authnServer)

	// Healthcheck endpoint
	healthService := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthService)
	healthService.SetServingStatus("spark-operator", grpc_health_v1.HealthCheckResponse_SERVING)

	return nil
}

func GetProtectedServices() []string {
	return []string{
		pbtree.SparkTreeService_ServiceDesc.ServiceName,
		pbinternal.SparkInternalService_ServiceDesc.ServiceName,
		pbtokeninternal.SparkTokenInternalService_ServiceDesc.ServiceName,
		pbgossip.GossipService_ServiceDesc.ServiceName,
	}
}
