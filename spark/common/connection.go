package common

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	sogrpc "github.com/lightsparkdev/spark/common/grpc"
	"github.com/lightsparkdev/spark/common/logging"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/otel/attribute"
)

// RetryPolicyConfig represents configuration for gRPC retry policy
type RetryPolicyConfig struct {
	MaxAttempts          int
	InitialBackoff       time.Duration
	MaxBackoff           time.Duration
	BackoffMultiplier    float64
	RetryableStatusCodes []string
}

// defaultRetryPolicy provides the default retry configuration
var defaultRetryPolicy = RetryPolicyConfig{
	MaxAttempts:          3,
	InitialBackoff:       1 * time.Second,
	MaxBackoff:           10 * time.Second,
	BackoffMultiplier:    2.0,
	RetryableStatusCodes: []string{"UNAVAILABLE"},
}

type ClientTimeoutConfig struct {
	TimeoutProvider sogrpc.TimeoutProvider
}

// createRetryPolicy generates a service config JSON string from a RetryPolicyConfig
func createRetryPolicy(config *RetryPolicyConfig) string {
	return fmt.Sprintf(`{
		"methodConfig": [{
		  "name": [{}],
		  "retryPolicy": {
			  "MaxAttempts": %d,
			  "InitialBackoff": "%s",
			  "MaxBackoff": "%s",
			  "BackoffMultiplier": %.1f,
			  "RetryableStatusCodes": [ "%s" ]
		  }
		}]}`, config.MaxAttempts, config.InitialBackoff.String(), config.MaxBackoff.String(),
		config.BackoffMultiplier, strings.Join(config.RetryableStatusCodes, "\", \""))
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

	logger := logging.GetLoggerFromContext(ctx)
	logging.ObserveServiceCall(ctx, method, duration)

	if err != nil {
		logger.Error("gRPC client request failed", "grpc_client_method", method, "grpc_client_duration", duration.Seconds(), "error", err)
	} else {
		logger.Info("gRPC client request succeeded", "grpc_client_method", method, "grpc_client_duration", duration.Seconds())
	}
	return err
}

func BasicClientOptions(address string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig) []grpc.DialOption {
	clientOpts := []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(
			otelgrpc.WithMetricAttributes(attribute.String("server.address", address)),
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
	if len(certPath) == 0 {
		return NewGRPCConnectionUnixDomainSocket(address, retryPolicy, clientTimeoutConfig)
	}

	clientOpts := BasicClientOptions(address, retryPolicy, clientTimeoutConfig)

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

	clientOpts = append(
		clientOpts,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: host == "localhost",
			RootCAs:            certPool,
			ServerName:         host,
		})),
	)

	return grpc.NewClient(address, clientOpts...)
}

// Will only attempt to connect to a unix domain socket address. If the address
// is not prefixed explicitly with "unix://", it will be prepended.
func NewGRPCConnectionUnixDomainSocket(address string, retryPolicy *RetryPolicyConfig, clientTimeoutConfig *ClientTimeoutConfig) (*grpc.ClientConn, error) {
	// Unix domain sockets always have a prefix of unix:// or unix: followed by
	// a path. So in practice, we need to accept either unix:///path/to/socket
	// or unix:/path/to/socket.
	if !strings.HasPrefix(address, "unix:///") && !strings.HasPrefix(address, "unix:/") {
		address = "unix://" + address
	}

	clientOpts := BasicClientOptions(address, retryPolicy, clientTimeoutConfig)
	// This is safe because we verified above that we are only connecting to a
	// unix domain socket, which are always secure, local connections.
	clientOpts = append(clientOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	return grpc.NewClient(address, clientOpts...)
}
