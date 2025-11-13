package so

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/common"
	sparkgrpc "github.com/lightsparkdev/spark/common/grpc"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/utils"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type OperatorClientConn interface {
	grpc.ClientConnInterface
	Close() error
}

// SigningOperator is the information about a signing operator.
type SigningOperator struct {
	// ID is the index of the signing operator.
	ID uint64
	// Identifier is the identifier of the signing operator, which will be index + 1 in 32 bytes big endian hex string.
	// Used as shamir secret share identifier in DKG key shares.
	Identifier string
	// AddressRpc is the address of the signing operator.
	AddressRpc string
	// Address is the address of the signing operator used for serving the DKG service.
	AddressDkg string
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
	// connPools caches pools per gRPC target address (RPC vs DKG).
	connPools map[string]*operatorConnPool
	// connPoolsMu guards connPools access.
	connPoolsMu sync.Mutex
}

type OperatorConnectionFactory interface {
	NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, clientTimeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error)
}

type operatorConnectionFactorySecure struct {
	operator *SigningOperator
}

func (o *operatorConnectionFactorySecure) NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, clientTimeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	extraOpts := []grpc.DialOption{
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
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
	}
	return common.NewGRPCConnectionWithOptions(address, o.operator.CertPath, retryPolicy, clientTimeoutConfig, extraOpts...)
}

func NewOperatorConnectionFactorySecure(operator *SigningOperator) OperatorConnectionFactory {
	return &operatorConnectionFactorySecure{operator: operator}
}

// jsonSigningOperator is used for JSON unmarshaling
type jsonSigningOperator struct {
	ID                uint32  `json:"id"`
	Address           string  `json:"address"`
	AddressDkg        *string `json:"address_dkg"`
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

	// Decode hex string to bytes
	pubKey, err := hex.DecodeString(js.IdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to decode public key hex: %w", err)
	}
	identityPubKey, err := keys.ParsePublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}
	s.IdentityPublicKey = identityPubKey

	s.ID = uint64(js.ID)
	s.Identifier = utils.IndexToIdentifier(js.ID)
	s.AddressRpc = js.Address
	if js.AddressDkg != nil {
		s.AddressDkg = *js.AddressDkg
	} else {
		s.AddressDkg = js.Address // Use the same address for DKG if not specified
	}
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
	pool, err := s.getOrCreateConnectionPool(address)
	if err != nil {
		return nil, err
	}
	return pool.getConnection()
}

func (s *SigningOperator) getOrCreateConnectionPool(address string) (*operatorConnPool, error) {
	s.connPoolsMu.Lock()
	defer s.connPoolsMu.Unlock()

	if s.connPools == nil {
		s.connPools = make(map[string]*operatorConnPool)
	}

	if pool, ok := s.connPools[address]; ok {
		return pool, nil
	}

	ocf := s.OperatorConnectionFactory
	if ocf == nil {
		ocf = &operatorConnectionFactorySecure{operator: s}
	}

	factory := func() (*grpc.ClientConn, error) {
		return ocf.NewGRPCConnection(address, nil, &s.ClientTimeoutConfig)
	}

	pool := newOperatorConnPool(factory, s.connPoolConfig, s.Logger)
	s.connPools[address] = pool
	return pool, nil
}

// NewOperatorGRPCConnection returns a pooled gRPC connection to the operator's RPC endpoint.
// Callers MUST close the returned connection to release it back to the pool.
func (s *SigningOperator) NewOperatorGRPCConnection() (OperatorClientConn, error) {
	return s.newGrpcConnection(s.AddressRpc)
}

// NewOperatorGRPCConnectionForDKG creates a DKG connection to the AddressDkg endpoint.
// Callers MUST close the returned connection to release it back to the pool.
func (s *SigningOperator) NewOperatorGRPCConnectionForDKG() (OperatorClientConn, error) {
	return s.newGrpcConnection(s.AddressDkg)
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
