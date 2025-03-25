package common

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// RetryPolicyConfig represents configuration for gRPC retry policy
type RetryPolicyConfig struct {
	MaxAttempts          int
	InitialBackoffSecs   time.Duration
	MaxBackoffSecs       time.Duration
	BackoffMultiplier    float64
	RetryableStatusCodes []string
}

// DefaultRetryPolicy provides the default retry configuration
var DefaultRetryPolicy = RetryPolicyConfig{
	MaxAttempts:          3,
	InitialBackoffSecs:   1 * time.Second,
	MaxBackoffSecs:       10 * time.Second,
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
		}]}`, config.MaxAttempts, config.InitialBackoffSecs.String(), config.MaxBackoffSecs.String(),
		config.BackoffMultiplier, strings.Join(config.RetryableStatusCodes, "\", \""))
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
	var serviceConfig string
	if retryPolicy != nil {
		serviceConfig = CreateRetryPolicy(*retryPolicy)
	} else {
		serviceConfig = CreateRetryPolicy(DefaultRetryPolicy)
	}

	var creds credentials.TransportCredentials
	if len(certPath) == 0 {
		return NewGRPCConnectionWithoutTLS(address, retryPolicy)
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

	creds = credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: host == "localhost",
		RootCAs:            certPool,
		ServerName:         host,
	})

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds), grpc.WithDefaultServiceConfig(serviceConfig))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewGRPCConnectionWithoutTLS(address string, retryPolicy *RetryPolicyConfig) (*grpc.ClientConn, error) {
	serviceConfig := CreateRetryPolicy(DefaultRetryPolicy)
	if retryPolicy != nil {
		serviceConfig = CreateRetryPolicy(*retryPolicy)
	}

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultServiceConfig(serviceConfig))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewGRPCConnectionWithTestTLS(address string, retryPolicy *RetryPolicyConfig) (*grpc.ClientConn, error) {
	serviceConfig := CreateRetryPolicy(DefaultRetryPolicy)
	if retryPolicy != nil {
		serviceConfig = CreateRetryPolicy(*retryPolicy)
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)), grpc.WithDefaultServiceConfig(serviceConfig))
	if err != nil {
		return nil, err
	}
	return conn, nil
}
