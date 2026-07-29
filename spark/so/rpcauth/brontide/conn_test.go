package brontide_test

import (
	"crypto/rand"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/rpcauth/brontide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectedPair runs a brontide handshake over net.Pipe() with passthrough inner credentials and returns the two
// resulting net.Conns once the handshake completes. The underlying raw pipe halves are also returned so tamper tests
// can inject bytes directly onto the wire.
type connectedPair struct {
	client    net.Conn
	server    net.Conn
	clientRaw net.Conn
	serverRaw net.Conn
}

func newConnectedPair(t *testing.T) connectedPair {
	t.Helper()

	clientPriv := keys.MustGeneratePrivateKeyFromRand(rand.Reader)
	serverPriv := keys.MustGeneratePrivateKeyFromRand(rand.Reader)
	clientPub := clientPriv.Public()
	peers := brontide.PeerLookupFunc(func(p keys.Public) *brontide.Peer {
		if p == clientPub {
			return &brontide.Peer{Identifier: "client", IdentityPublicKey: p}
		}
		return nil
	})

	res, err := handshakeOverPipe(t,
		brontide.ClientConfig{
			Inner:           passthroughCreds{},
			LocalPrivateKey: clientPriv,
			RemotePublicKey: serverPriv.Public(),
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

	return connectedPair{
		client:    res.clientConn,
		server:    res.serverConn,
		clientRaw: res.clientRaw,
		serverRaw: res.serverRaw,
	}
}

func TestWrappedConnRoundTrip(t *testing.T) {
	t.Run("empty write is a no-op", func(t *testing.T) {
		p := newConnectedPair(t)
		n, err := p.client.Write(nil)
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	t.Run("empty read returns immediately without blocking", func(t *testing.T) {
		p := newConnectedPair(t)

		done := make(chan struct{})
		var n int
		var err error
		go func() {
			n, err = p.server.Read(nil)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Read(nil) blocked instead of returning immediately")
		}
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	t.Run("partial reads serve from the buffered record", func(t *testing.T) {
		p := newConnectedPair(t)
		payload := []byte("hello world from the brontide wrapper")

		writeErr := make(chan error, 1)
		go func() {
			_, err := p.client.Write(payload)
			writeErr <- err
		}()

		// Read in small chunks; brontide.Conn-style buffering should serve all reads from a single underlying record.
		received := make([]byte, 0, len(payload))
		buf := make([]byte, 4)
		for len(received) < len(payload) {
			n, err := p.server.Read(buf)
			require.NoError(t, err)
			received = append(received, buf[:n]...)
		}
		require.NoError(t, <-writeErr)
		assert.Equal(t, payload, received)
	})
}

func TestWrappedConnChunkBoundaries(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"one byte below record max", math.MaxUint16 - 1},
		{"exactly record max", math.MaxUint16},
		{"one byte above record max forces split", math.MaxUint16 + 1},
		{"three records", 3 * math.MaxUint16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newConnectedPair(t)
			payload := make([]byte, tc.size)
			_, err := rand.Read(payload)
			require.NoError(t, err)

			writeErr := make(chan error, 1)
			go func() {
				_, err := p.client.Write(payload)
				writeErr <- err
			}()

			received := make([]byte, tc.size)
			_, err = io.ReadFull(p.server, received)
			require.NoError(t, err)
			require.NoError(t, <-writeErr)
			assert.Equal(t, payload, received)
		})
	}
}

func TestWrappedConnPassthroughs(t *testing.T) {
	p := newConnectedPair(t)

	t.Run("addresses delegate to inner conn", func(t *testing.T) {
		// net.Pipe ends report a synthetic "pipe" network. We should hand it back unchanged from the inner conn.
		assert.Equal(t, "pipe", p.client.LocalAddr().Network())
		assert.Equal(t, "pipe", p.client.RemoteAddr().Network())
	})

	t.Run("deadline setters delegate to inner conn", func(t *testing.T) {
		// net.Pipe supports deadlines, so each setter should succeed without error.
		// We're checking that the wrapper doesn't invent its own semantics, not that net.Pipe enforces the deadline.
		deadline := time.Now().Add(time.Second)
		require.NoError(t, p.client.SetDeadline(deadline))
		require.NoError(t, p.client.SetReadDeadline(deadline))
		require.NoError(t, p.client.SetWriteDeadline(deadline))
	})
}

func TestWrappedConnTamperDetection(t *testing.T) {
	p := newConnectedPair(t)

	// Bypass the wrappedConn on the client side and shove garbage straight onto the raw pipe. The server's wrappedConn
	// will try to decrypt it as a brontide record and the AEAD MAC check on the encrypted length header fails.
	garbage := make([]byte, 64)
	_, err := rand.Read(garbage)
	require.NoError(t, err)
	go func() {
		_, _ = p.clientRaw.Write(garbage)
	}()
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := p.server.Read(buf)
		readErr <- err
	}()
	select {
	case err := <-readErr:
		require.Error(t, err, "expected AEAD failure when raw conn is fed garbage")
	case <-t.Context().Done():
		t.Fatal("tamper-read did not return in time")
	}
}
