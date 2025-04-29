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

	"github.com/lightsparkdev/spark/common/logging"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// RetryPolicyConfig represents configuration for gRPC retry policy
type RetryPolicyConfig struct {
	MaxAttempts          int
	InitialBackoff       time.Duration
	MaxBackoff           time.Duration
	BackoffMultiplier    float64
	RetryableStatusCodes []string
}

// DefaultRetryPolicy provides the default retry configuration
var DefaultRetryPolicy = RetryPolicyConfig{
	MaxAttempts:          3,
	InitialBackoff:       1 * time.Second,
	MaxBackoff:           10 * time.Second,
	BackoffMultiplier:    2.0,
	RetryableStatusCodes: []string{"UNAVAILABLE"},
}

// CreateRetryPolicy generates a service config JSON string from a RetryPolicyConfig
func CreateRetryPolicy(config RetryPolicyConfig) string {
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

func LoggingUnaryClientInterceptor(
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

// NewGRPCConnection creates a new gRPC connection to the given address. If certPath is nil, it
// will create a connection without TLS.
func NewGRPCConnection(address string, certPath *string, retryPolicy *RetryPolicyConfig) (*grpc.ClientConn, error) {
	if certPath == nil {
		return NewGRPCConnectionWithoutTLS(address, retryPolicy)
	}
	return NewGRPCConnectionWithCert(address, *certPath, retryPolicy)
}

// NewGRPCConnection creates a new gRPC connection to the given address.
func NewGRPCConnectionWithCert(address string, certPath string, retryPolicy *RetryPolicyConfig) (*grpc.ClientConn, error) {
	if len(certPath) == 0 {
		return NewGRPCConnectionWithoutTLS(address, retryPolicy)
	}

	clientOpts := []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithUnaryInterceptor(LoggingUnaryClientInterceptor),
	}

	if retryPolicy != nil {
		clientOpts = append(clientOpts, grpc.WithDefaultServiceConfig(CreateRetryPolicy(*retryPolicy)))
	} else {
		clientOpts = append(clientOpts, grpc.WithDefaultServiceConfig(CreateRetryPolicy(DefaultRetryPolicy)))
	}

	certPool := x509.NewCertPool()
	serverCert, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}

	if !certPool.AppendCertsFromPEM(serverCert) {
		return nil, errors.New("failed to append certificate")
	}

	url, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	host := url.Hostname()
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

	conn, err := grpc.NewClient(address, clientOpts...)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewGRPCConnectionWithoutTLS(address string, retryPolicy *RetryPolicyConfig) (*grpc.ClientConn, error) {
	clientOpts := []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithUnaryInterceptor(LoggingUnaryClientInterceptor),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	if retryPolicy != nil {
		clientOpts = append(clientOpts, grpc.WithDefaultServiceConfig(CreateRetryPolicy(*retryPolicy)))
	} else {
		clientOpts = append(clientOpts, grpc.WithDefaultServiceConfig(CreateRetryPolicy(DefaultRetryPolicy)))
	}

	conn, err := grpc.NewClient(address, clientOpts...)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewGRPCConnectionWithTestTLS(address string, retryPolicy *RetryPolicyConfig) (*grpc.ClientConn, error) {
	clientOpts := []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: true,
		})),
	}

	if retryPolicy != nil {
		clientOpts = append(clientOpts, grpc.WithDefaultServiceConfig(CreateRetryPolicy(*retryPolicy)))
	} else {
		clientOpts = append(clientOpts, grpc.WithDefaultServiceConfig(CreateRetryPolicy(DefaultRetryPolicy)))
	}

	conn, err := grpc.NewClient(address, clientOpts...)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
