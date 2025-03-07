package common

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var retryPolicy = `{
		"methodConfig": [{
		  "name": [{}],
		  "retryPolicy": {
			  "MaxAttempts": 3,
			  "InitialBackoff": "1s",
			  "MaxBackoff": "10s",
			  "BackoffMultiplier": 2.0,
			  "RetryableStatusCodes": [ "UNAVAILABLE" ]
		  }
		}]}`

// NewGRPCConnection creates a new gRPC connection to the given address.
func NewGRPCConnectionWithCert(address string, certPath string) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if len(certPath) == 0 {
		return NewGRPCConnectionWithoutTLS(address)
	} else {
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
	}

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds), grpc.WithDefaultServiceConfig(retryPolicy))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewGRPCConnectionWithoutTLS(address string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultServiceConfig(retryPolicy))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewGRPCConnectionWithTestTLS(address string) (*grpc.ClientConn, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)), grpc.WithDefaultServiceConfig(retryPolicy))
	if err != nil {
		return nil, err
	}
	return conn, nil
}
