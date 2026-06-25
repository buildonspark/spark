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
	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
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

// RegisterPublicGrpcServers registers services that handle traffic from outside the SO cluster. These services accept
// connections authenticated by user identity tokens or run unauthenticated; they don't speak the brontide handshake.
func RegisterPublicGrpcServers(
	grpcServer *grpc.Server,
	args *args,
	config *so.Config,
	logger *zap.Logger,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier,
	eventsRouter *events.EventRouter,
	rwClient *partner.RisingWaveClient,
) error {
	if args.RunningLocally {
		mockServer := sparkgrpc.NewMockServer(config, dbClient, ephemeralDBClient)
		pbmock.RegisterMockServiceServer(grpcServer, mockServer)
	}

	sparkServer := sparkgrpc.NewSparkServer(config, eventsRouter)
	pbspark.RegisterSparkServiceServer(grpcServer, sparkServer)

	// Public partner endpoint
	sparkPartnerServer := sparkgrpc.NewSparkPartnerServer(rwClient)
	pbpartner.RegisterSparkPartnerServiceServer(grpcServer, sparkPartnerServer)

	// Public SO token endpoint
	sparkTokenServer := sparkgrpc.NewSparkTokenServer(config, config, dbClient)
	pbtoken.RegisterSparkTokenServiceServer(grpcServer, sparkTokenServer)

	authnServer, err := sparkgrpc.NewAuthnServer(sparkgrpc.AuthnServerConfig{
		IdentityPrivateKey: config.IdentityPrivateKey,
		ChallengeTimeout:   args.ChallengeTimeout,
		SessionDuration:    args.SessionDuration,
	}, sessionTokenCreatorVerifier)
	if err != nil {
		return fmt.Errorf("failed to create authentication server: %w", err)
	}
	pbauthn.RegisterSparkAuthnServiceServer(grpcServer, authnServer)

	return nil
}

// RegisterInternalGrpcServers registers services that only ever talk to other SOs.
// When the operator is deployed with a separate internal listener, these services live behind TLS+brontide mutual auth.
// When the internal listener is disabled, they are registered on the same server as the public services and protected
// by the existing IP allowlist alone.
func RegisterInternalGrpcServers(
	grpcServer *grpc.Server,
	args *args,
	config *so.Config,
	frostClient *grpc.ClientConn,
	dbClient *ent.Client,
) error {
	if !args.DisableDKG {
		dkgServer := dkg.NewServer(frostClient, config)
		pbdkg.RegisterDKGServiceServer(grpcServer, dkgServer)
	}

	sparkInternalServer := sparkgrpc.NewSparkInternalServer(config)
	pbinternal.RegisterSparkInternalServiceServer(grpcServer, sparkInternalServer)

	gossipServer := sparkgrpc.NewGossipServer(config)
	pbgossip.RegisterGossipServiceServer(grpcServer, gossipServer)

	sparkTokenInternalServer := sparkgrpc.NewSparkTokenInternalServer(config, dbClient)
	pbtokeninternal.RegisterSparkTokenInternalServiceServer(grpcServer, sparkTokenInternalServer)

	return nil
}
