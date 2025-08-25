package frost

import (
	"google.golang.org/grpc"

	"github.com/lightsparkdev/spark/common"
)

type FrostGRPCConnectionFactory interface {
	NewFrostGRPCConnection(signerAddress string) (*grpc.ClientConn, error)
}

type FrostGRPCConnectionFactorySecure struct{}

func (f *FrostGRPCConnectionFactorySecure) NewFrostGRPCConnection(signerAddress string) (*grpc.ClientConn, error) {
	return common.NewGRPCConnectionUnixDomainSocket(signerAddress, nil, nil)
}
