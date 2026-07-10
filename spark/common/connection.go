package common

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	sogrpc "github.com/lightsparkdev/spark/common/grpc"
	"github.com/lightsparkdev/spark/common/logging"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// RetryPolicyConfig represents configuration for the gRPC service config
// applied to a client connection (retry policy + optional load balancing
// policy). Kept named RetryPolicyConfig for backwards compatibility with
// existing call sites.
type RetryPolicyConfig struct {
	MaxAttempts          int
	InitialBackoff       time.Duration
	MaxBackoff           time.Duration
	BackoffMultiplier    float64
	RetryableStatusCodes []string
	// LoadBalancingPolicy, if non-empty, is emitted as the top-level
	// "loadBalancingPolicy" field of the service config. Empty falls back to
	// gRPC's default (pick_first). Setting this here instead of via a
	// separate grpc.WithDefaultServiceConfig prevents the retry policy from
	// being silently overwritten: grpc-go's WithDefaultServiceConfig is a
	// single-pointer setter, so the last call wins.
	LoadBalancingPolicy string
}

// defaultRetryPolicy provides the default retry configuration for pooled
// client connections (SO-to-SO and other operator traffic).
var defaultRetryPolicy = RetryPolicyConfig{
	MaxAttempts:          3,
	InitialBackoff:       1 * time.Second,
	MaxBackoff:           10 * time.Second,
	BackoffMultiplier:    2.0,
	RetryableStatusCodes: []string{"UNAVAILABLE"},
	LoadBalancingPolicy:  "round_robin",
}

type ClientTimeoutConfig struct {
	TimeoutProvider sogrpc.TimeoutProvider
}

// createRetryPolicy generates a gRPC service config JSON string from a
// RetryPolicyConfig. The emitted JSON contains a methodConfig with the
// retry policy applied to all methods and, when set, a top-level
// loadBalancingPolicy.
func createRetryPolicy(config *RetryPolicyConfig) string {
	type retryPolicyJSON struct {
		MaxAttempts          int      `json:"MaxAttempts"`
		InitialBackoff       string   `json:"InitialBackoff"`
		MaxBackoff           string   `json:"MaxBackoff"`
		BackoffMultiplier    float64  `json:"BackoffMultiplier"`
		RetryableStatusCodes []string `json:"RetryableStatusCodes"`
	}
	type methodConfigJSON struct {
		Name        []struct{}      `json:"name"`
		RetryPolicy retryPolicyJSON `json:"retryPolicy"`
	}
	type serviceConfigJSON struct {
		LoadBalancingPolicy string             `json:"loadBalancingPolicy,omitempty"`
		MethodConfig        []methodConfigJSON `json:"methodConfig"`
	}

	sc := serviceConfigJSON{
		LoadBalancingPolicy: config.LoadBalancingPolicy,
		MethodConfig: []methodConfigJSON{{
			Name: []struct{}{{}},
			RetryPolicy: retryPolicyJSON{
				MaxAttempts:          config.MaxAttempts,
				InitialBackoff:       config.InitialBackoff.String(),
				MaxBackoff:           config.MaxBackoff.String(),
				BackoffMultiplier:    config.BackoffMultiplier,
				RetryableStatusCodes: config.RetryableStatusCodes,
			},
		}},
	}
	b, err := json.Marshal(sc)
	if err != nil {
		// Fields are all concrete, finite types; Marshal cannot fail.
		panic(fmt.Sprintf("marshal service config: %v", err))
	}
	return string(b)
}

func loggingUnaryClientInterceptor(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	start := time.Now()
	err := invoker(ctx, method, req, reply, cc, opts...)
	duration := time.Since(start)

	logger := logging.GetLoggerFromContext(ctx).With(zap.String("grpc_client_method", method))
	logging.ObserveServiceCall(ctx, method, duration)

	if err != nil {
		logger.Error("gRPC client request failed", zap.Error(err))
	}
	return err
}

// IdempotencyKeyHeader is the gRPC metadata key used for idempotency.
// Referenced by both the client interceptor (here) and the server-side
// IdempotencyInterceptor in so/grpc.
const IdempotencyKeyHeader = "x-idempotency-key"

// IdempotencyKeyClientInterceptor returns a gRPC client interceptor that
// attaches a unique UUID as the x-idempotency-key metadata header on each call.
// gRPC transport-level retries preserve the metadata, so the same key is reused
// on retry, allowing the server-side IdempotencyInterceptor to return cached
// responses instead of re-executing the handler.
func IdempotencyKeyClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		key := uuid.New().String()
		ctx = metadata.AppendToOutgoingContext(ctx, IdempotencyKeyHeader, key)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func BasicClientOptions(address string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig) []grpc.DialOption {
	clientOpts := []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(
			otelgrpc.WithMetricAttributes(attribute.String("server.address", address)),
			otelgrpc.WithSpanAttributes(attribute.String("server.address", address)),
		)),
	}

	interceptors := []grpc.UnaryClientInterceptor{
		loggingUnaryClientInterceptor,
	}

	if clientTimeoutConfig != nil {
		interceptors = append(interceptors, sogrpc.ClientTimeoutInterceptor(clientTimeoutConfig.TimeoutProvider))
	}

	rp := &defaultRetryPolicy
	if retryPolicy != nil {
		rp = retryPolicy
	}
	clientOpts = append(clientOpts, grpc.WithDefaultServiceConfig(createRetryPolicy(rp)), grpc.WithChainUnaryInterceptor(interceptors...))

	return clientOpts
}

// Creates a secure gRPC connection to the given address. If certPath is empty, it will create a connection to the
// address as a Unix domain socket (which is a secure connection). If address is not a Unix domain socket, it will
// return an error.
func NewGRPCConnection(address string, certPath string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig) (*grpc.ClientConn, error) {
	return newGRPCConnection(address, certPath, retryPolicy, clientTimeoutConfig, nil)
}

func NewGRPCConnectionWithOptions(address string, certPath string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig, extra ...grpc.DialOption) (*grpc.ClientConn, error) {
	return newGRPCConnection(address, certPath, retryPolicy, clientTimeoutConfig, extra)
}

func newGRPCConnection(address string, certPath string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig, extra []grpc.DialOption) (*grpc.ClientConn, error) {
	if len(certPath) == 0 {
		return newGRPCConnectionUnixDomainSocket(address, retryPolicy, clientTimeoutConfig, extra)
	}

	creds, err := BuildTLSCredentialsFromCert(address, certPath)
	if err != nil {
		return nil, err
	}
	return NewGRPCConnectionWithCredentials(address, creds, retryPolicy, clientTimeoutConfig, extra...)
}

// BuildTLSCredentialsFromCert loads a PEM-encoded server certificate from certPath and returns gRPC TransportCredentials
// configured to verify the server using it. The address is parsed for its hostname, with a localhost-skip fallback for
// tests.
//
// Exposed so callers that need to wrap the TLS credentials in a higher-level transport can reuse the same trust-anchor
// loading logic without duplicating it.
func BuildTLSCredentialsFromCert(address string, certPath string) (credentials.TransportCredentials, error) {
	certPool := x509.NewCertPool()
	serverCert, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	if !certPool.AppendCertsFromPEM(serverCert) {
		return nil, errors.New("failed to append certificate")
	}

	parsedURL, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	host := parsedURL.Hostname()
	if strings.Contains(address, "localhost") {
		host = "localhost"
	}

	return credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: host == "localhost",
		RootCAs:            certPool,
		ServerName:         host,
	}), nil
}

// NewGRPCConnectionWithCredentials dials address with the supplied TransportCredentials, layering on the same basic
// client options used by the cert-based helpers. Use this when the caller has already constructed credentials.
func NewGRPCConnectionWithCredentials(address string, creds credentials.TransportCredentials, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig, extra ...grpc.DialOption) (*grpc.ClientConn, error) {
	clientOpts := BasicClientOptions(address, retryPolicy, clientTimeoutConfig)
	clientOpts = append(clientOpts, extra...)
	clientOpts = append(clientOpts, grpc.WithTransportCredentials(creds))
	return grpc.NewClient(address, clientOpts...)
}

// Will only attempt to connect to a unix domain socket address. If the address
// is not prefixed explicitly with "unix://", it will be prepended.
func NewGRPCConnectionUnixDomainSocket(address string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig) (*grpc.ClientConn, error) {
	return newGRPCConnectionUnixDomainSocket(address, retryPolicy, clientTimeoutConfig, nil)
}

func newGRPCConnectionUnixDomainSocket(address string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig, extra []grpc.DialOption) (*grpc.ClientConn, error) {
	// Unix domain sockets always have a prefix of unix:// or unix: followed by
	// a path. So in practice, we need to accept either unix:///path/to/socket
	// or unix:/path/to/socket.
	if !strings.HasPrefix(address, "unix:///") && !strings.HasPrefix(address, "unix:/") {
		address = "unix://" + address
	}

	clientOpts := BasicClientOptions(address, retryPolicy, clientTimeoutConfig)
	clientOpts = append(clientOpts, extra...)
	// This is safe because we verified above that we are only connecting to a
	// unix domain socket, which are always secure, local connections.
	clientOpts = append(clientOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	return grpc.NewClient(address, clientOpts...)
}
