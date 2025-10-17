package schematype

import (
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// Network is the network type.
type Network string

const (
	NetworkUnspecified Network = "UNSPECIFIED"
	NetworkMainnet     Network = "MAINNET"
	NetworkRegtest     Network = "REGTEST"
	NetworkTestnet     Network = "TESTNET"
	NetworkSignet      Network = "SIGNET"
)

// MarshalProto converts a Network to a spark protobuf Network.
func (n Network) MarshalProto() (pb.Network, error) {
	switch n {
	case NetworkMainnet:
		return pb.Network_MAINNET, nil
	case NetworkRegtest:
		return pb.Network_REGTEST, nil
	case NetworkTestnet:
		return pb.Network_TESTNET, nil
	case NetworkSignet:
		return pb.Network_SIGNET, nil
	default:
		return pb.Network_UNSPECIFIED, sparkerrors.InternalTypeConversionError(fmt.Errorf("unknown network: %s", n))
	}
}

// UnmarshalProto converts a spark protobuf Network to a Network.
func (n *Network) UnmarshalProto(proto pb.Network) error {
	switch proto {
	case pb.Network_MAINNET:
		*n = NetworkMainnet
	case pb.Network_REGTEST:
		*n = NetworkRegtest
	case pb.Network_TESTNET:
		*n = NetworkTestnet
	case pb.Network_SIGNET:
		*n = NetworkSignet
	default:
		return sparkerrors.InternalTypeConversionError(fmt.Errorf("unknown network: %d", proto))
	}
	return nil
}

// Values returns the values for the Network type.
func (Network) Values() []string {
	return []string{
		string(NetworkUnspecified),
		string(NetworkMainnet),
		string(NetworkRegtest),
		string(NetworkTestnet),
		string(NetworkSignet),
	}
}
