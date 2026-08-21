package brontide

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/lightningnetwork/lnd/brontide"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightsparkdev/spark/common/keys"
	"google.golang.org/grpc/credentials"
)

// defaultHandshakeTimeout bounds each act of the brontide handshake when no deadline is set on the underlying connection.
// Lifted from brontide's handshakeReadTimeout (5s) and applied to the whole handshake rather than per-act, since that's
// the gRPC TransportCredentials convention.
const defaultHandshakeTimeout = 10 * time.Second

// SecurityProtocol is the value reported by ProtocolInfo.SecurityProtocol.
const SecurityProtocol = "spark-brontide"

// ClientConfig parameterizes a brontide TransportCredentials in initiator (client) mode. One ClientConfig is created per
// remote peer because the peer's static identity public key is bound into the handshake digest.
type ClientConfig struct {
	// Inner is the underlying TransportCredentials that handles the outer TLS handshake (server cert verification).
	// This is typically created via credentials.NewTLS(tlsConfig).
	Inner credentials.TransportCredentials

	// LocalPrivateKey is this operator's identity private key, used as our static Noise key. Required.
	LocalPrivateKey keys.Private

	// RemotePublicKey is the target operator's identity public key. The brontide handshake will fail if the responder
	// can't prove it has the matching private key. Required.
	RemotePublicKey keys.Public

	// HandshakeTimeout bounds the brontide handshake (in addition to any deadline carried on the context passed to
	// ClientHandshake). If zero, defaultHandshakeTimeout is used.
	HandshakeTimeout time.Duration
}

// ServerConfig parameterizes a brontide TransportCredentials in responder (server) mode. A single ServerConfig serves
// all incoming connections. The peer identity is recovered from the handshake and checked against Peers.
type ServerConfig struct {
	// Inner is the TransportCredentials that handles the outer TLS handshake (presenting the server cert).
	// This is typically created via credentials.NewTLS(tlsConfig).
	Inner credentials.TransportCredentials

	// LocalPrivateKey is this operator's identity private key, used as our static Noise key. Required.
	LocalPrivateKey keys.Private

	// Peers resolves a handshake-authenticated public key to a known operator. Connections from keys not present in
	// Peers are rejected after the handshake completes. Required.
	Peers PeerLookup

	// HandshakeTimeout bounds the brontide handshake. If zero, defaultHandshakeTimeout is used.
	HandshakeTimeout time.Duration
}

// NewClientCredentials constructs TransportCredentials for dialing a single known operator over TLS + brontide.
func NewClientCredentials(cfg ClientConfig) (credentials.TransportCredentials, error) {
	if cfg.Inner == nil {
		return nil, fmt.Errorf("brontide: client credentials require an inner TLS TransportCredentials")
	}
	if cfg.LocalPrivateKey.IsZero() {
		return nil, fmt.Errorf("brontide: client credentials require LocalPrivateKey")
	}
	if cfg.RemotePublicKey.IsZero() {
		return nil, fmt.Errorf("brontide: client credentials require RemotePublicKey")
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = defaultHandshakeTimeout
	}
	return &clientCreds{cfg: cfg}, nil
}

// NewServerCredentials constructs TransportCredentials for accepting brontide connections from any operator listed in cfg.Peers.
func NewServerCredentials(cfg ServerConfig) (credentials.TransportCredentials, error) {
	if cfg.Inner == nil {
		return nil, fmt.Errorf("brontide: server credentials require an inner TLS TransportCredentials")
	}
	if cfg.LocalPrivateKey.IsZero() {
		return nil, fmt.Errorf("brontide: server credentials require LocalPrivateKey")
	}
	if cfg.Peers == nil {
		return nil, fmt.Errorf("brontide: server credentials require a PeerLookup")
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = defaultHandshakeTimeout
	}
	return &serverCreds{cfg: cfg}, nil
}

type clientCreds struct {
	cfg ClientConfig
}

func (c *clientCreds) ClientHandshake(ctx context.Context, authority string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	tlsConn, tlsAuthInfo, err := c.cfg.Inner.ClientHandshake(ctx, authority, raw)
	if err != nil {
		return nil, nil, fmt.Errorf("brontide: inner TLS handshake: %w", err)
	}
	// The inner is typically credentials.NewTLS(...) which always produces credentials.TLSInfo, but the type system
	// lets callers pass any TransportCredentials. If the inner isn't TLS-shaped (e.g. some test double), tlsInfo stays
	// zero and we just don't surface TLS state on AuthInfo, since brontide's mutual auth doesn't depend on it.
	tlsInfo, _ := tlsAuthInfo.(credentials.TLSInfo)

	deadline := handshakeDeadline(ctx, c.cfg.HandshakeTimeout)
	if err := tlsConn.SetDeadline(deadline); err != nil {
		_ = tlsConn.Close()
		return nil, nil, fmt.Errorf("brontide: set handshake deadline: %w", err)
	}

	local := newLocalECDH(c.cfg.LocalPrivateKey)
	remote := c.cfg.RemotePublicKey.ToBTCEC()
	m := brontide.NewBrontideMachine(true, local, remote)

	// Wire ctx cancellation to the conn so blocking brontide IO aborts promptly rather than running to the
	// handshake-period deadline. TransportCredentials.ClientHandshake is required to honor ctx cancellation. Otherwise,
	// a canceled dial can hang up to HandshakeTimeout. We synchronize with watcherDone before clearing the deadline
	// below to guarantee the watcher cannot SetDeadline(time.Now()) after our SetDeadline(time.Time{}) clear, which
	// would otherwise poison the conn.
	handshakeDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = tlsConn.SetDeadline(time.Now())
		case <-handshakeDone:
		}
	}()

	handshakeErr := runInitiatorHandshake(tlsConn, m)
	close(handshakeDone)
	<-watcherDone

	if handshakeErr != nil {
		_ = tlsConn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, fmt.Errorf("brontide: client handshake canceled: %w", ctxErr)
		}
		return nil, nil, fmt.Errorf("brontide: initiator handshake: %w", handshakeErr)
	}

	// Clear the handshake-period deadline before returning. gRPC's client transport doesn't clear deadlines after
	// ClientHandshake, so anything we leave on the conn becomes a poison pill on the long-lived channel, meaning
	// every HTTP/2 read or write past HandshakeTimeout would start failing with i/o timeout. This is the opposite of
	// the server side, where gRPC's connectionTimeout flow clears the deadline itself.
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		_ = tlsConn.Close()
		return nil, nil, fmt.Errorf("brontide: clear handshake deadline: %w", err)
	}

	info := AuthInfo{
		SecurityLevel: credentials.PrivacyAndIntegrity,
		Peer:          &Peer{IdentityPublicKey: c.cfg.RemotePublicKey},
		TLS:           tlsInfo,
	}
	return newWrappedConn(tlsConn, m), info, nil
}

func (c *clientCreds) ServerHandshake(net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, fmt.Errorf("brontide: client credentials cannot be used as a server")
}

func (c *clientCreds) Info() credentials.ProtocolInfo {
	inner := c.cfg.Inner.Info()
	inner.SecurityProtocol = SecurityProtocol
	return inner
}

func (c *clientCreds) Clone() credentials.TransportCredentials {
	clone := *c
	clone.cfg.Inner = c.cfg.Inner.Clone()
	return &clone
}

// OverrideServerName is deprecated upstream but still required by the credentials.TransportCredentials interface.
// We delegate to the inner TLS credentials so behavior matches what gRPC would see without our wrapper.
func (c *clientCreds) OverrideServerName(name string) error {
	return c.cfg.Inner.OverrideServerName(name)
}

type serverCreds struct {
	cfg ServerConfig
}

func (s *serverCreds) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, fmt.Errorf("brontide: server credentials cannot be used as a client")
}

func (s *serverCreds) ServerHandshake(raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	tlsConn, tlsAuthInfo, err := s.cfg.Inner.ServerHandshake(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("brontide: inner TLS handshake: %w", err)
	}
	// The inner is typically credentials.NewTLS(...) which always produces credentials.TLSInfo, but the type system
	// lets callers pass any TransportCredentials. If the inner isn't TLS-shaped, tlsInfo stays zero and we just don't
	// surface TLS state on AuthInfo, since brontide's mutual auth doesn't depend on it.
	tlsInfo, _ := tlsAuthInfo.(credentials.TLSInfo)

	deadline := time.Now().Add(s.cfg.HandshakeTimeout)
	if err := tlsConn.SetDeadline(deadline); err != nil {
		_ = tlsConn.Close()
		return nil, nil, fmt.Errorf("brontide: set handshake deadline: %w", err)
	}

	local := newLocalECDH(s.cfg.LocalPrivateKey)
	m := brontide.NewBrontideMachine(false, local, nil)

	if err := runResponderHandshake(tlsConn, m); err != nil {
		_ = tlsConn.Close()
		return nil, nil, fmt.Errorf("brontide: responder handshake: %w", err)
	}

	remotePub, ok := machineRemoteStatic(m)
	if !ok {
		_ = tlsConn.Close()
		return nil, nil, fmt.Errorf("brontide: handshake completed without remote static key")
	}

	peerInfo := s.cfg.Peers.LookupPeer(remotePub)
	if peerInfo == nil {
		_ = tlsConn.Close()
		return nil, nil, fmt.Errorf("brontide: unknown peer %s", remotePub)
	}

	// Intentionally don't clear the conn deadline here. When this runs inside grpc.Server, gRPC sets a connectionTimeout
	// deadline on the raw conn before calling ServerHandshake and continues to rely on it while reading the HTTP/2
	// preface after we return. It clears the deadline itself once the transport's fully set up.
	//
	// Calling SetDeadline(time.Time{}) here would let an authenticated peer complete the handshake and then stall,
	// leaving the gRPC goroutine blocked indefinitely on the preface read. Our handshake-window deadline stays in
	// effect as a tighter bound until gRPC clears it, which is the safer side to err on.

	info := AuthInfo{
		SecurityLevel: credentials.PrivacyAndIntegrity,
		Peer:          peerInfo,
		TLS:           tlsInfo,
	}
	return newWrappedConn(tlsConn, m), info, nil
}

func (s *serverCreds) Info() credentials.ProtocolInfo {
	inner := s.cfg.Inner.Info()
	inner.SecurityProtocol = SecurityProtocol
	return inner
}

func (s *serverCreds) Clone() credentials.TransportCredentials {
	clone := *s
	clone.cfg.Inner = s.cfg.Inner.Clone()
	return &clone
}

// OverrideServerName is deprecated upstream but still required by the credentials.TransportCredentials interface; we
// delegate to the inner TLS credentials so behavior matches what gRPC would see without the wrapper.
func (s *serverCreds) OverrideServerName(name string) error {
	return s.cfg.Inner.OverrideServerName(name)
}

// runInitiatorHandshake performs Acts 1-3 from the initiator side.
func runInitiatorHandshake(conn net.Conn, m *brontide.Machine) error {
	actOne, err := m.GenActOne()
	if err != nil {
		return fmt.Errorf("gen act one: %w", err)
	}
	if _, err := conn.Write(actOne[:]); err != nil {
		return fmt.Errorf("write act one: %w", err)
	}

	var actTwo [brontide.ActTwoSize]byte
	if _, err := io.ReadFull(conn, actTwo[:]); err != nil {
		return fmt.Errorf("read act two: %w", err)
	}
	if err := m.RecvActTwo(actTwo); err != nil {
		return fmt.Errorf("recv act two: %w", err)
	}

	actThree, err := m.GenActThree()
	if err != nil {
		return fmt.Errorf("gen act three: %w", err)
	}
	if _, err := conn.Write(actThree[:]); err != nil {
		return fmt.Errorf("write act three: %w", err)
	}
	return nil
}

// runResponderHandshake performs Acts 1-3 from the responder side. After this returns nil, the responder has
// authenticated the initiator, and the machine is in steady state; machineRemoteStatic(m) yields the initiator's static
// public key.
func runResponderHandshake(conn net.Conn, m *brontide.Machine) error {
	var actOne [brontide.ActOneSize]byte
	if _, err := io.ReadFull(conn, actOne[:]); err != nil {
		return fmt.Errorf("read act one: %w", err)
	}
	if err := m.RecvActOne(actOne); err != nil {
		return fmt.Errorf("recv act one: %w", err)
	}

	actTwo, err := m.GenActTwo()
	if err != nil {
		return fmt.Errorf("gen act two: %w", err)
	}
	if _, err := conn.Write(actTwo[:]); err != nil {
		return fmt.Errorf("write act two: %w", err)
	}

	var actThree [brontide.ActThreeSize]byte
	if _, err := io.ReadFull(conn, actThree[:]); err != nil {
		return fmt.Errorf("read act three: %w", err)
	}
	if err := m.RecvActThree(actThree); err != nil {
		return fmt.Errorf("recv act three: %w", err)
	}
	return nil
}

// handshakeDeadline returns the earlier of ctx's deadline (if any) and
// now+timeout. Used to honor caller-provided deadlines on ClientHandshake.
func handshakeDeadline(ctx context.Context, timeout time.Duration) time.Time {
	fallback := time.Now().Add(timeout)
	if ctx == nil {
		return fallback
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.After(fallback) {
		return fallback
	}
	return deadline
}

// Assert that our adapters satisfy the brontide & gRPC interfaces.
var (
	_ credentials.TransportCredentials = (*clientCreds)(nil)
	_ credentials.TransportCredentials = (*serverCreds)(nil)
	_ keychain.SingleKeyECDH           = (*keychain.PrivKeyECDH)(nil)
)
