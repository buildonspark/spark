package so

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/common"
	sparkgrpc "github.com/lightsparkdev/spark/common/grpc"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type OperatorClientConn interface {
	grpc.ClientConnInterface
	Close() error
}

// connTransport distinguishes the credential flavor a pool dials with, so pools for different transports never
// collide even when they target the same address.
type connTransport string

const (
	// transportTLS is a plain-TLS dial via OperatorConnectionFactory (public listener).
	transportTLS connTransport = "tls"
	// transportBrontide is a brontide dial via internalConnFactory (internal-only listener).
	transportBrontide connTransport = "brontide"
)

// connPoolKey identifies a connection pool by both transport and target address.
type connPoolKey struct {
	transport connTransport
	address   string
}

// SigningOperator is the information about a signing operator.
type SigningOperator struct {
	// ID is the index of the signing operator.
	ID uint64
	// Identifier is the identifier of the signing operator, which will be index + 1 in 32 bytes big endian hex string.
	// Used as shamir secret share identifier in DKG key shares.
	Identifier string
	// AddressRpc is the address of the signing operator on the public listener.
	AddressRpc string
	// Address is the address of the signing operator used for serving the DKG service.
	AddressDkg string
	// InternalAddress is the address of the brontide-protected internal listener (host:port).
	// Required when this operator is dialed using brontide credentials; unused otherwise.
	InternalAddress string
	// IdentityPublicKey is the identity public key of the signing operator.
	IdentityPublicKey keys.Public
	// ServerCertPath is the path to the server certificate.
	CertPath string
	// ExternalAddress is the external address of the signing operator.
	ExternalAddress string
	// Generates connections to the signing operator. By default, will use
	// OperatorConnectionFactorySecure, but this allows the setting of alternate
	// connection types, generally for testing.
	OperatorConnectionFactory OperatorConnectionFactory
	// ClientTimeoutConfig is the configuration for the client timeout's knob service and defaulttimeout length
	ClientTimeoutConfig common.ClientTimeoutConfig
	// Logger is used for logging connection pool events. If nil, a no-op logger is used.
	Logger *zap.Logger
	// connPoolConfig holds the pool configuration for outbound gRPC connections.
	connPoolConfig OperatorConnectionPoolConfig
	// connPools caches pools per (transport, target address). Keying on transport as well as address keeps the
	// plain-TLS and brontide pools separate even if InternalAddress aliases AddressRpc/AddressDkg (e.g. via the
	// AddressDkg defaulting in UnmarshalJSON or a misconfiguration) — otherwise the first dial's factory would be
	// silently reused for the other transport.
	connPools map[connPoolKey]*operatorConnPool
	// connPoolsMu guards connPools access.
	connPoolsMu sync.Mutex
	// internalConnFactory, if non-nil, is used by NewOperatorInternalGRPCConnection and NewOperatorGRPCConnectionForDKG
	// to dial this peer's internal-only listener. EnableBrontideClient installs it; otherwise nil and internal-flavored
	// callers fall through to OperatorConnectionFactory + AddressRpc.
	//
	// Kept separate from OperatorConnectionFactory because that factory is also used by cross-operator SparkService
	// calls and must keep dialing AddressRpc with plain TLS regardless of brontide mode.
	internalConnFactory OperatorConnectionFactory
	// brontideAvailable is true when brontide has been provisioned for this peer. It's a necessary but not sufficient
	// condition for dialing over brontide: the internal-flavored connection methods additionally require the
	// KnobInternalRPCBrontideEnabled knob to be on.
	brontideAvailable bool
}

type OperatorConnectionFactory interface {
	NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, clientTimeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error)
}

type operatorConnectionFactorySecure struct {
	operator *SigningOperator
}

func (o *operatorConnectionFactorySecure) NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, clientTimeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	extraOpts := []grpc.DialOption{
		// Spec-compliant client pings; server currently has no enforcement policy.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(10*1024*1024),
			grpc.MaxCallSendMsgSize(10*1024*1024),
		),
		grpc.WithInitialWindowSize(1 << 20),      // 1 MB
		grpc.WithInitialConnWindowSize(16 << 20), // 16 MB
		grpc.WithChainUnaryInterceptor(common.IdempotencyKeyClientInterceptor()),
	}
	return common.NewGRPCConnectionWithOptions(address, o.operator.CertPath, retryPolicy, clientTimeoutConfig, extraOpts...)
}

func NewOperatorConnectionFactorySecure(operator *SigningOperator) OperatorConnectionFactory {
	return &operatorConnectionFactorySecure{operator: operator}
}

// SetInternalConnectionFactory installs the factory used for internal-flavored dials
// (NewOperatorInternalGRPCConnection, NewOperatorGRPCConnectionForDKG) when brontide is enabled. It's a seam for
// tests that inject a mock connection factory: without it, a test that flips brontide on would route around the
// injected OperatorConnectionFactory and dial the real InternalAddress. It does not set brontideAvailable — that
// remains gated by brontide provisioning.
func (s *SigningOperator) SetInternalConnectionFactory(factory OperatorConnectionFactory) {
	s.internalConnFactory = factory
}

// jsonSigningOperator is used for JSON unmarshaling
type jsonSigningOperator struct {
	ID                uint32  `json:"id"`
	Address           string  `json:"address"`
	AddressDkg        *string `json:"address_dkg"`
	InternalAddress   string  `json:"internal_address"`
	IdentityPublicKey string  `json:"identity_public_key"`
	CertPath          string  `json:"cert_path"`
	ExternalAddress   string  `json:"external_address"`
}

// UnmarshalJSON implements json.Unmarshaler interface
func (s *SigningOperator) UnmarshalJSON(data []byte) error {
	var js jsonSigningOperator
	if err := json.Unmarshal(data, &js); err != nil {
		return err
	}

	identityPubKey, err := keys.ParsePublicKeyHex(js.IdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}
	s.IdentityPublicKey = identityPubKey

	s.ID = uint64(js.ID)
	s.Identifier = IndexToIdentifier(js.ID)
	s.AddressRpc = js.Address
	if js.AddressDkg != nil {
		s.AddressDkg = *js.AddressDkg
	} else {
		s.AddressDkg = js.Address // Use the same address for DKG if not specified
	}
	s.InternalAddress = js.InternalAddress
	s.CertPath = js.CertPath
	s.ExternalAddress = js.ExternalAddress
	s.OperatorConnectionFactory = NewOperatorConnectionFactorySecure(s)
	s.connPoolConfig = DefaultOperatorConnPoolConfig()
	return nil
}

// MarshalProto marshals the signing operator to a protobuf message.
func (s *SigningOperator) MarshalProto() *pb.SigningOperatorInfo {
	return &pb.SigningOperatorInfo{
		Index:      s.ID,
		Identifier: s.Identifier,
		PublicKey:  s.IdentityPublicKey.Serialize(),
		Address:    s.ExternalAddress,
	}
}

func (s *SigningOperator) newGrpcConnection(address string) (OperatorClientConn, error) {
	return s.newGrpcConnectionVia(transportTLS, s.OperatorConnectionFactory, address)
}

// newGrpcConnectionVia is the same as newGrpcConnection but lets pool creators pick the transport and factory used for
// first-time dials on a given address.
func (s *SigningOperator) newGrpcConnectionVia(transport connTransport, factory OperatorConnectionFactory, address string) (OperatorClientConn, error) {
	pool, err := s.getOrCreateConnectionPool(transport, factory, address)
	if err != nil {
		return nil, err
	}
	return pool.getConnection()
}

func (s *SigningOperator) getOrCreateConnectionPool(transport connTransport, factory OperatorConnectionFactory, address string) (*operatorConnPool, error) {
	s.connPoolsMu.Lock()
	defer s.connPoolsMu.Unlock()

	if s.connPools == nil {
		s.connPools = make(map[connPoolKey]*operatorConnPool)
	}

	key := connPoolKey{transport: transport, address: address}
	if pool, ok := s.connPools[key]; ok {
		return pool, nil
	}

	if factory == nil {
		factory = &operatorConnectionFactorySecure{operator: s}
	}

	dial := func() (*grpc.ClientConn, error) {
		return factory.NewGRPCConnection(address, nil, &s.ClientTimeoutConfig)
	}

	pool := newOperatorConnPool(dial, s.connPoolConfig, s.Logger)
	s.connPools[key] = pool
	return pool, nil
}

// NewOperatorGRPCConnection returns a pooled plain-TLS gRPC connection to the peer's public listener (AddressRpc).
// This is the right method for cross-operator calls into SparkService (e.g. QueryNodes during tree-sync) and for any
// other RPC that targets a service registered on the public listener. Internal-only services should use
// NewOperatorInternalGRPCConnection.
//
// Callers MUST close the returned connection to release it back to the pool.
func (s *SigningOperator) NewOperatorGRPCConnection() (OperatorClientConn, error) {
	return s.newGrpcConnection(s.AddressRpc)
}

// NewOperatorInternalGRPCConnection returns a pooled gRPC connection for SO-to-SO internal-only services. It routes
// over brontide when enabled; otherwise it falls back to plain TLS against the public listener.
//
// Callers MUST close the returned connection to release it back to the pool.
func (s *SigningOperator) NewOperatorInternalGRPCConnection(ctx context.Context) (OperatorClientConn, error) {
	if s.brontideEnabled(ctx) {
		return s.newGrpcConnectionVia(transportBrontide, s.internalConnFactory, s.InternalAddress)
	}
	s.evictBrontidePoolIfDisabled(ctx)
	return s.NewOperatorGRPCConnection()
}

// NewOperatorGRPCConnectionForDKG creates a connection for the DKG service. Like NewOperatorInternalGRPCConnection, it
// routes over brontide only when brontide is provisioned and the KnobInternalRPCBrontideEnabled knob is on.
//
// Callers MUST close the returned connection to release it back to the pool.
func (s *SigningOperator) NewOperatorGRPCConnectionForDKG(ctx context.Context) (OperatorClientConn, error) {
	if s.brontideEnabled(ctx) {
		return s.newGrpcConnectionVia(transportBrontide, s.internalConnFactory, s.InternalAddress)
	}
	s.evictBrontidePoolIfDisabled(ctx)
	return s.newGrpcConnection(s.AddressDkg)
}

// brontideEnabled reports whether internal RPCs to this peer should be dialed over brontide right now.
func (s *SigningOperator) brontideEnabled(ctx context.Context) bool {
	return s.brontideAvailable && knobs.GetKnobsService(ctx).GetValue(knobs.KnobInternalRPCBrontideEnabled, 0) > 0
}

// evictBrontidePoolIfDisabled tears down a previously-created brontide pool once the kill-switch has routed internal
// RPCs back to plain TLS. Without this, the brontide pool and its open connections would linger for the process
// lifetime — nothing acquires from it after the flip, and the pool only reaps connections on access, so even idle
// ones (with their keepalive pings) would never be closed. That defeats the point of a kill-switch flipped because
// brontide connections are hanging or broken.
//
// Only the brontide pool is evicted: the plain-TLS AddressRpc pool is shared with cross-operator SparkService calls
// and must survive. Gated on brontideAvailable so plain-TLS-only deployments never take the lock.
func (s *SigningOperator) evictBrontidePoolIfDisabled(ctx context.Context) {
	if !s.brontideAvailable {
		return
	}
	// Re-check the knob: brontideEnabled is (available && knob), and we're on the not-enabled branch, so the knob is
	// off here — but read it directly to stay correct if brontideEnabled's definition changes.
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobInternalRPCBrontideEnabled, 0) > 0 {
		return
	}
	s.evictConnPool(connPoolKey{transport: transportBrontide, address: s.InternalAddress})
}

// evictConnPool removes the pool for key, if present, and closes it in the background (its graceful drain waits for
// in-flight borrowers, so it must not block the acquisition path).
func (s *SigningOperator) evictConnPool(key connPoolKey) {
	s.connPoolsMu.Lock()
	pool, ok := s.connPools[key]
	if ok {
		delete(s.connPools, key)
	}
	s.connPoolsMu.Unlock()

	if ok && pool != nil {
		go pool.Close()
	}
}

// SetTimeoutProvider sets the timeout provider for this signing operator.
func (s *SigningOperator) SetTimeoutProvider(timeoutProvider sparkgrpc.TimeoutProvider) {
	s.ClientTimeoutConfig = common.ClientTimeoutConfig{
		TimeoutProvider: timeoutProvider,
	}
}

// SetConnectionPoolLimits configures the min/max connection counts for this operator.
func (s *SigningOperator) SetConnectionPoolLimits(minConnections, maxConnections int) {
	cfg := OperatorConnectionPoolConfig{
		MinConnections:        minConnections,
		MaxConnections:        maxConnections,
		IdleTimeout:           s.connPoolConfig.IdleTimeout,
		MaxLifetime:           s.connPoolConfig.MaxLifetime,
		UsersPerConnectionCap: s.connPoolConfig.UsersPerConnectionCap,
		ScaleConcurrency:      s.connPoolConfig.ScaleConcurrency,
	}
	s.SetConnectionPoolConfig(cfg)
}

// SetConnectionPoolConfig updates the current pool configuration without dropping existing connections.
func (s *SigningOperator) SetConnectionPoolConfig(cfg OperatorConnectionPoolConfig) {
	cfg = cfg.WithDefaults()
	if s.connPoolConfig.Equal(cfg) {
		return
	}

	s.connPoolConfig = cfg

	s.connPoolsMu.Lock()
	defer s.connPoolsMu.Unlock()
	for _, pool := range s.connPools {
		if pool != nil {
			pool.updateConfig(cfg)
		}
	}
}
