package brontide_test

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/rpcauth/brontide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
)

// passthroughCreds is an inner TransportCredentials used in tests that want to exercise the brontide handshake without
// also dragging real TLS into every test. It hands back the raw conn unchanged and reports PrivacyAndIntegrity to keep
// the wrapper's invariants happy.
type passthroughCreds struct{}

func (passthroughCreds) ClientHandshake(_ context.Context, _ string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return raw, tlsInfo(), nil
}

func (passthroughCreds) ServerHandshake(raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return raw, tlsInfo(), nil
}

func tlsInfo() credentials.TLSInfo {
	return credentials.TLSInfo{CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.PrivacyAndIntegrity}}
}

func (passthroughCreds) Info() credentials.ProtocolInfo { return credentials.ProtocolInfo{} }
func (p passthroughCreds) Clone() credentials.TransportCredentials {
	return p
}

func (passthroughCreds) OverrideServerName(string) error { return nil }

// handshakeOverPipe runs a client + server handshake concurrently against the supplied configs over net.Pipe(), and
// returns the resulting net.Conn / AuthInfo pairs (or the first error encountered). The raw pipe halves are also
// returned so tests that need to inject bytes onto the wire (e.g. tamper detection) can do so.
type handshakeResult struct {
	clientConn net.Conn
	clientInfo credentials.AuthInfo
	clientRaw  net.Conn
	serverConn net.Conn
	serverInfo credentials.AuthInfo
	serverRaw  net.Conn
}

func handshakeOverPipe(t *testing.T, clientCfg brontide.ClientConfig, serverCfg brontide.ServerConfig) (handshakeResult, error) {
	t.Helper()
	clientCreds, err := brontide.NewClientCredentials(clientCfg)
	require.NoError(t, err)
	serverCreds, err := brontide.NewServerCredentials(serverCfg)
	require.NoError(t, err)

	rawA, rawB := net.Pipe()

	type out struct {
		conn net.Conn
		info credentials.AuthInfo
		err  error
	}
	serverCh := make(chan out, 1)
	clientCh := make(chan out, 1)

	go func() {
		c, info, err := serverCreds.ServerHandshake(rawB)
		serverCh <- out{c, info, err}
	}()
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		c, info, err := clientCreds.ClientHandshake(ctx, "test-authority", rawA)
		clientCh <- out{c, info, err}
	}()

	deadline := time.After(5 * time.Second)
	var clientRes, serverRes out
	for range 2 {
		select {
		case clientRes = <-clientCh:
			if clientRes.err != nil {
				return handshakeResult{}, clientRes.err
			}
		case serverRes = <-serverCh:
			if serverRes.err != nil {
				return handshakeResult{}, serverRes.err
			}
		case <-deadline:
			return handshakeResult{}, errors.New("handshake timed out")
		}
	}
	return handshakeResult{
		clientConn: clientRes.conn,
		clientInfo: clientRes.info,
		clientRaw:  rawA,
		serverConn: serverRes.conn,
		serverInfo: serverRes.info,
		serverRaw:  rawB,
	}, nil
}

func TestHandshakeRoundTrip(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	clientPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	serverPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	clientPub := clientPriv.Public()
	serverPub := serverPriv.Public()

	peers := brontide.PeerLookupFunc(func(p keys.Public) *brontide.Peer {
		if p == clientPub {
			return &brontide.Peer{Identifier: "client-op", IdentityPublicKey: p}
		}
		return nil
	})

	res, err := handshakeOverPipe(t,
		brontide.ClientConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: clientPriv,
			RemotePublicKey: serverPub,
		},
		brontide.ServerConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: serverPriv,
			Peers:           peers,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = res.clientConn.Close()
		_ = res.serverConn.Close()
	})

	t.Run("server resolves the calling operator identity", func(t *testing.T) {
		srvInfo, ok := res.serverInfo.(brontide.AuthInfo)
		require.True(t, ok, "server AuthInfo is %T", res.serverInfo)
		require.NotNil(t, srvInfo.Peer)
		assert.Equal(t, "client-op", srvInfo.Peer.Identifier)
		assert.Equal(t, clientPub, srvInfo.Peer.IdentityPublicKey)
		assert.Equal(t, "spark-brontide", srvInfo.AuthType())
	})

	t.Run("client AuthInfo reflects the dialed responder pubkey", func(t *testing.T) {
		cliInfo, ok := res.clientInfo.(brontide.AuthInfo)
		require.True(t, ok, "client AuthInfo is %T", res.clientInfo)
		require.NotNil(t, cliInfo.Peer)
		assert.Equal(t, serverPub, cliInfo.Peer.IdentityPublicKey)
	})

	t.Run("post-handshake conns carry encrypted application bytes", func(t *testing.T) {
		clientErr := make(chan error, 1)
		go func() {
			_, err := res.clientConn.Write([]byte("ping"))
			clientErr <- err
		}()
		buf := make([]byte, 4)
		_, err := io.ReadFull(res.serverConn, buf)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(buf))
		require.NoError(t, <-clientErr)
	})

	t.Run("post-handshake conns survive payloads spanning multiple AEAD records", func(t *testing.T) {
		// 200KB forces brontide to split the write into multiple 64KB-1 records.
		payload := make([]byte, 200*1024)
		_, err := rng.Read(payload)
		require.NoError(t, err)

		writeErr := make(chan error, 1)
		go func() {
			_, err := res.clientConn.Write(payload)
			writeErr <- err
		}()
		got := make([]byte, len(payload))
		_, err = io.ReadFull(res.serverConn, got)
		require.NoError(t, err)
		require.NoError(t, <-writeErr)
		assert.Equal(t, payload, got)
	})
}

func TestServerRejectsUnknownPeer(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	clientPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	serverPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	serverPub := serverPriv.Public()

	emptyPeers := brontide.PeerLookupFunc(func(keys.Public) *brontide.Peer { return nil })

	_, err := handshakeOverPipe(t,
		brontide.ClientConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: clientPriv,
			RemotePublicKey: serverPub,
		},
		brontide.ServerConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: serverPriv,
			Peers:           emptyPeers,
		},
	)
	require.ErrorContains(t, err, "unknown peer")
}

func TestClientRejectsWrongResponderKey(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	clientPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	serverPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	wrongServerPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	peers := brontide.PeerLookupFunc(func(keys.Public) *brontide.Peer {
		return &brontide.Peer{Identifier: "any", IdentityPublicKey: clientPriv.Public()}
	})

	// Client dials wrongServerPub, but the server actually holds serverPriv. Noise_XK binds the responder static into
	// the handshake digest, so the client's RecvActTwo decryption fails.
	_, err := handshakeOverPipe(t,
		brontide.ClientConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: clientPriv,
			RemotePublicKey: wrongServerPub,
		},
		brontide.ServerConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: serverPriv,
			Peers:           peers,
		},
	)
	require.Error(t, err)
}

func TestNewClientCredentialsRejectsIncompleteConfig(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	priv := keys.MustGeneratePrivateKeyFromRand(rng)
	pub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	t.Run("missing inner credentials", func(t *testing.T) {
		_, err := brontide.NewClientCredentials(brontide.ClientConfig{
			LocalPrivateKey: priv,
			RemotePublicKey: pub,
		})
		require.ErrorContains(t, err, "inner TLS TransportCredentials")
	})

	t.Run("missing local private key", func(t *testing.T) {
		_, err := brontide.NewClientCredentials(brontide.ClientConfig{
			Inner:           passthroughCreds{},
			RemotePublicKey: pub,
		})
		require.ErrorContains(t, err, "LocalPrivateKey")
	})

	t.Run("missing remote public key", func(t *testing.T) {
		_, err := brontide.NewClientCredentials(brontide.ClientConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: priv,
		})
		require.ErrorContains(t, err, "RemotePublicKey")
	})
}

func TestNewServerCredentialsRejectsIncompleteConfig(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	priv := keys.MustGeneratePrivateKeyFromRand(rng)
	peers := brontide.PeerLookupFunc(func(keys.Public) *brontide.Peer { return nil })

	t.Run("missing inner credentials", func(t *testing.T) {
		_, err := brontide.NewServerCredentials(brontide.ServerConfig{
			LocalPrivateKey: priv,
			Peers:           peers,
		})
		require.ErrorContains(t, err, "inner TLS TransportCredentials")
	})

	t.Run("missing local private key", func(t *testing.T) {
		_, err := brontide.NewServerCredentials(brontide.ServerConfig{
			Inner: passthroughCreds{},
			Peers: peers,
		})
		require.ErrorContains(t, err, "LocalPrivateKey")
	})

	t.Run("missing peer lookup", func(t *testing.T) {
		_, err := brontide.NewServerCredentials(brontide.ServerConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: priv,
		})
		require.ErrorContains(t, err, "PeerLookup")
	})
}
