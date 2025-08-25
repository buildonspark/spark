package sparktesting

import (
	"crypto/tls"

	"github.com/lightsparkdev/spark/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func DangerousNewGRPCConnectionWithoutTLS(address string, retryPolicy *common.RetryPolicyConfig) (*grpc.ClientConn, error) {
	clientOpts := common.BasicClientOptions(address, retryPolicy, nil)
	// !!!DANGER DANGER WILL ROBINSON!!!
	clientOpts = append(clientOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	return grpc.NewClient(address, clientOpts...)
}

func DangerousNewGRPCConnectionWithoutVerifyTLS(address string, retryPolicy *common.RetryPolicyConfig) (*grpc.ClientConn, error) {
	clientOpts := common.BasicClientOptions(address, retryPolicy, nil)
	// !!!DANGER DANGER HIGH VOLTAGE!!!
	clientOpts = append(clientOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true,
	})))

	return grpc.NewClient(address, clientOpts...)
}
