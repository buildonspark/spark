package brontide_test

// End-to-end test that runs a real gRPC server over TLS+brontide and dials it from a real gRPC client.
// Catches regressions where the handshake works in isolation but the wrapper interacts badly with grpc-go's HTTP/2
// preface, deadline handling, or AuthInfo plumbing.

import (
	"context"
	"math/rand/v2"
	"net"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/rpcauth/brontide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestBrontideEndToEndOverRealGRPC(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	clientPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	serverPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	clientPub := clientPriv.Public()

	peers := brontide.PeerLookupFunc(func(p keys.Public) *brontide.Peer {
		if p == clientPub {
			return &brontide.Peer{Identifier: "client", IdentityPublicKey: p}
		}
		return nil
	})

	serverCreds, err := brontide.NewServerCredentials(brontide.ServerConfig{
		Inner:           passthroughCreds{},
		LocalPrivateKey: serverPriv,
		Peers:           peers,
	})
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	srv := grpc.NewServer(grpc.Creds(serverCreds))
	grpc_health_v1.RegisterHealthServer(srv, health.NewServer())
	t.Cleanup(srv.GracefulStop)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(listener)
	}()

	clientCreds, err := brontide.NewClientCredentials(brontide.ClientConfig{
		Inner:           passthroughCreds{},
		LocalPrivateKey: clientPriv,
		RemotePublicKey: serverPriv.Public(),
	})
	require.NoError(t, err)

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(clientCreds))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.GetStatus())

	// Confirm Serve didn't exit early with anything other than the graceful-stop path.
	select {
	case err := <-serveErr:
		// grpc.Server.Serve returns nil on GracefulStop; a non-nil here would mean Serve died mid-test.
		require.NoError(t, err)
	default:
		// Still serving; Cleanup will GracefulStop.
	}
}

func TestBrontideEndToEndRejectsUnknownClient(t *testing.T) {
	// Here, the server's PeerLookup doesn't know the client. The handshake completes the Noise exchange, but the
	// post-handshake operator-identity lookup fails, the server closes the connection, and the client's RPC raises an error.
	rng := rand.NewChaCha8([32]byte{})
	clientPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	serverPriv := keys.MustGeneratePrivateKeyFromRand(rng)

	emptyPeers := brontide.PeerLookupFunc(func(keys.Public) *brontide.Peer { return nil })

	serverCreds, err := brontide.NewServerCredentials(brontide.ServerConfig{
		Inner:           passthroughCreds{},
		LocalPrivateKey: serverPriv,
		Peers:           emptyPeers,
	})
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	srv := grpc.NewServer(grpc.Creds(serverCreds))
	grpc_health_v1.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.GracefulStop)

	clientCreds, err := brontide.NewClientCredentials(brontide.ClientConfig{
		Inner:           passthroughCreds{},
		LocalPrivateKey: clientPriv,
		RemotePublicKey: serverPriv.Public(),
	})
	require.NoError(t, err)

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(clientCreds))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	require.Error(t, err, "expected RPC to fail when the server rejects the unknown peer")
}
