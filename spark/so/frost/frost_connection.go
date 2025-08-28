package frost

import (
	"google.golang.org/grpc"

	"github.com/lightsparkdev/spark/common"
	sparkgrpc "github.com/lightsparkdev/spark/common/grpc"
)

type FrostGRPCConnectionFactory interface {
	NewFrostGRPCConnection(signerAddress string) (*grpc.ClientConn, error)
	SetTimeoutProvider(timeoutProvider sparkgrpc.TimeoutProvider)
}

type frostGRPCConnectionFactorySecure struct {
	ClientTimeoutConfig *common.ClientTimeoutConfig
}

func NewFrostGRPCConnectionFactorySecure() *frostGRPCConnectionFactorySecure {
	return &frostGRPCConnectionFactorySecure{
		ClientTimeoutConfig: nil,
	}
}

func (f *frostGRPCConnectionFactorySecure) SetTimeoutProvider(timeoutProvider sparkgrpc.TimeoutProvider) {
	f.ClientTimeoutConfig = &common.ClientTimeoutConfig{
		TimeoutProvider: timeoutProvider,
	}
}

func (f *frostGRPCConnectionFactorySecure) NewFrostGRPCConnection(signerAddress string) (*grpc.ClientConn, error) {
	return common.NewGRPCConnectionUnixDomainSocket(signerAddress, nil, f.ClientTimeoutConfig)
}
