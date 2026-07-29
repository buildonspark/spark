package brontide_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/rpcauth/brontide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
)

func newClientCredsForTest(t *testing.T) credentials.TransportCredentials {
	t.Helper()
	c, err := brontide.NewClientCredentials(brontide.ClientConfig{
		Inner:           passthroughCreds{},
		LocalPrivateKey: keys.MustGeneratePrivateKeyFromRand(rand.Reader),
		RemotePublicKey: keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public(),
	})
	require.NoError(t, err)
	return c
}

func newServerCredsForTest(t *testing.T) credentials.TransportCredentials {
	t.Helper()
	c, err := brontide.NewServerCredentials(brontide.ServerConfig{
		Inner:           passthroughCreds{},
		LocalPrivateKey: keys.MustGeneratePrivateKeyFromRand(rand.Reader),
		Peers:           brontide.PeerLookupFunc(func(keys.Public) *brontide.Peer { return nil }),
	})
	require.NoError(t, err)
	return c
}

func TestClientCredsRejectsServerHandshake(t *testing.T) {
	c := newClientCredsForTest(t)
	rawA, _ := testScopedPipe(t)

	_, _, err := c.ServerHandshake(rawA)
	require.ErrorContains(t, err, "cannot be used as a server")
}

func TestServerCredsRejectsClientHandshake(t *testing.T) {
	c := newServerCredsForTest(t)
	rawA, _ := testScopedPipe(t)

	_, _, err := c.ClientHandshake(t.Context(), "test", rawA)
	require.ErrorContains(t, err, "cannot be used as a client")
}

func TestCredentialsInfoReportsBrontideProtocol(t *testing.T) {
	t.Run("client", func(t *testing.T) {
		assert.Equal(t, brontide.SecurityProtocol, newClientCredsForTest(t).Info().SecurityProtocol)
	})
	t.Run("server", func(t *testing.T) {
		assert.Equal(t, brontide.SecurityProtocol, newServerCredsForTest(t).Info().SecurityProtocol)
	})
}

func TestCredentialsCloneIsIndependent(t *testing.T) {
	t.Run("client", func(t *testing.T) {
		c := newClientCredsForTest(t)
		clone := c.Clone()
		require.NotSame(t, c, clone)
		assert.Equal(t, c.Info().SecurityProtocol, clone.Info().SecurityProtocol)
	})
	t.Run("server", func(t *testing.T) {
		c := newServerCredsForTest(t)
		clone := c.Clone()
		require.NotSame(t, c, clone)
		assert.Equal(t, c.Info().SecurityProtocol, clone.Info().SecurityProtocol)
	})
}

// overrideTrackingCreds records OverrideServerName calls so we can verify the brontide wrapper forwards them to the inner credentials.
type overrideTrackingCreds struct {
	passthroughCreds
	serverName string
}

func (o *overrideTrackingCreds) OverrideServerName(name string) error {
	o.serverName = name
	return nil
}

func (o *overrideTrackingCreds) Clone() credentials.TransportCredentials {
	clone := *o
	return &clone
}

func TestOverrideServerNameDelegatesToInner(t *testing.T) {
	priv := keys.MustGeneratePrivateKeyFromRand(rand.Reader)
	pub := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	t.Run("client", func(t *testing.T) {
		inner := &overrideTrackingCreds{}
		c, err := brontide.NewClientCredentials(brontide.ClientConfig{
			Inner:           inner,
			LocalPrivateKey: priv,
			RemotePublicKey: pub,
		})
		require.NoError(t, err)
		require.NoError(t, c.OverrideServerName("example.test"))
		assert.Equal(t, "example.test", inner.serverName)
	})

	t.Run("server", func(t *testing.T) {
		inner := &overrideTrackingCreds{}
		c, err := brontide.NewServerCredentials(brontide.ServerConfig{
			Inner:           inner,
			LocalPrivateKey: priv,
			Peers:           brontide.PeerLookupFunc(func(keys.Public) *brontide.Peer { return nil }),
		})
		require.NoError(t, err)
		require.NoError(t, c.OverrideServerName("example.test"))
		assert.Equal(t, "example.test", inner.serverName)
	})
}

// failingInnerCreds returns a fixed error from its handshake methods, so inner-TLS failures propagate.
type failingInnerCreds struct{ err error }

func (f failingInnerCreds) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, f.err
}

func (f failingInnerCreds) ServerHandshake(net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, f.err
}
func (failingInnerCreds) Info() credentials.ProtocolInfo { return credentials.ProtocolInfo{} }
func (f failingInnerCreds) Clone() credentials.TransportCredentials {
	return f
}

func (failingInnerCreds) OverrideServerName(string) error { return nil }

func TestInnerHandshakeFailurePropagates(t *testing.T) {
	innerErr := errors.New("inner exploded")
	priv := keys.MustGeneratePrivateKeyFromRand(rand.Reader)
	pub := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	t.Run("client", func(t *testing.T) {
		c, err := brontide.NewClientCredentials(brontide.ClientConfig{
			Inner:           failingInnerCreds{err: innerErr},
			LocalPrivateKey: priv,
			RemotePublicKey: pub,
		})
		require.NoError(t, err)
		rawA, _ := testScopedPipe(t)

		_, _, err = c.ClientHandshake(t.Context(), "test", rawA)

		require.ErrorIs(t, err, innerErr)
	})

	t.Run("server", func(t *testing.T) {
		c, err := brontide.NewServerCredentials(brontide.ServerConfig{
			Inner:           failingInnerCreds{err: innerErr},
			LocalPrivateKey: priv,
			Peers:           brontide.PeerLookupFunc(func(keys.Public) *brontide.Peer { return nil }),
		})
		require.NoError(t, err)
		rawA, _ := testScopedPipe(t)

		_, _, err = c.ServerHandshake(rawA)
		require.ErrorIs(t, err, innerErr)
	})
}

func testScopedPipe(t *testing.T) (net.Conn, net.Conn) {
	rawA, rawB := net.Pipe()
	t.Cleanup(func() {
		_ = rawA.Close()
		_ = rawB.Close()
	})
	return rawA, rawB
}

func TestClientHandshakeRespectsContextCancellation(t *testing.T) {
	// The peer side never reads or writes, so the brontide Act One write / Act Two read blocks. Without ctx wiring,
	// the call would only unblock when HandshakeTimeout expires. We verify it returns promptly when ctx is canceled.
	c, err := brontide.NewClientCredentials(brontide.ClientConfig{
		Inner:            passthroughCreds{},
		LocalPrivateKey:  keys.MustGeneratePrivateKeyFromRand(rand.Reader),
		RemotePublicKey:  keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public(),
		HandshakeTimeout: 15 * time.Second, // generous; we expect cancellation to win
	})
	require.NoError(t, err)

	rawA, _ := testScopedPipe(t)
	ctx, cancel := context.WithCancel(t.Context())

	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, _, err := c.ClientHandshake(ctx, "test", rawA)
		done <- result{err}
	}()

	// Give the handshake a moment to enter the blocking IO, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		require.ErrorIs(t, res.err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("ClientHandshake did not return promptly after ctx cancellation; ctx wiring is broken")
	}
}
