//go:build !lightspark

package main

import (
	"fmt"

	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbauthn "github.com/lightsparkdev/spark/proto/spark_authn"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbtoken "github.com/lightsparkdev/spark/proto/spark_token"
	pbtokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authninternal"
	"github.com/lightsparkdev/spark/so/dkg"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entephemeral"
	sparkgrpc "github.com/lightsparkdev/spark/so/grpc"
	"github.com/lightsparkdev/spark/so/partner"
	events "github.com/lightsparkdev/spark/so/stream"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func RegisterGrpcServers(
	grpcServer *grpc.Server,
	args *args,
	config *so.Config,
	logger *zap.Logger,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	frostClient *grpc.ClientConn,
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier,
	eventsRouter *events.EventRouter,
) (cleanup func(), err error) {
	if args.RunningLocally {
		mockServer := sparkgrpc.NewMockServer(config, dbClient, ephemeralDBClient)
		pbmock.RegisterMockServiceServer(grpcServer, mockServer)
	}

	if !args.DisableDKG {
		dkgServer := dkg.NewServer(frostClient, config)
		pbdkg.RegisterDKGServiceServer(grpcServer, dkgServer)
	}

	// Private/Internal SO <-> SO endpoint
	sparkInternalServer := sparkgrpc.NewSparkInternalServer(config)
	pbinternal.RegisterSparkInternalServiceServer(grpcServer, sparkInternalServer)

	// Create RisingWave client (connects lazily on first query).
	rwClient := partner.NewRisingWaveClient(config.RisingWaveDSN)
	cleanup = func() {
		if rwClient != nil {
			_ = rwClient.Close()
		}
	}

	// Public SO endpoint
	sparkServer := sparkgrpc.NewSparkServer(config, eventsRouter, rwClient)
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

	// Public ID challenge auth endpoint
	authnServer, err := sparkgrpc.NewAuthnServer(sparkgrpc.AuthnServerConfig{
		IdentityPrivateKey: config.IdentityPrivateKey,
		ChallengeTimeout:   args.ChallengeTimeout,
		SessionDuration:    args.SessionDuration,
	}, sessionTokenCreatorVerifier)
	if err != nil {
		return cleanup, fmt.Errorf("failed to create authentication server: %w", err)
	}
	pbauthn.RegisterSparkAuthnServiceServer(grpcServer, authnServer)

	return cleanup, nil
}

func GetProtectedServices() []string {
	return []string{
		pbdkg.DKGService_ServiceDesc.ServiceName,
		pbinternal.SparkInternalService_ServiceDesc.ServiceName,
		pbtokeninternal.SparkTokenInternalService_ServiceDesc.ServiceName,
		pbgossip.GossipService_ServiceDesc.ServiceName,
	}
}
