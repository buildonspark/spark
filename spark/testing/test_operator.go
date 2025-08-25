package sparktesting

import (
	"github.com/lightsparkdev/spark/common"
	"google.golang.org/grpc"
)

type DangerousTestOperatorConnectionFactoryNoTLS struct{}

func (o *DangerousTestOperatorConnectionFactoryNoTLS) NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, clientTimeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	return DangerousNewGRPCConnectionWithoutTLS(address, retryPolicy)
}

type DangerousTestOperatorConnectionFactoryNoVerifyTLS struct{}

func (o *DangerousTestOperatorConnectionFactoryNoVerifyTLS) NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, clientTimeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	return DangerousNewGRPCConnectionWithoutVerifyTLS(address, retryPolicy)
}
