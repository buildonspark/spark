package btcnetwork

import (
	"database/sql/driver"
	"fmt"
	"strings"

	sparkerrors "github.com/lightsparkdev/spark/so/errors"

	"github.com/btcsuite/btcd/chaincfg"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// Network is the type for Bitcoin networks used with the operator.
type Network int

const (
	Unspecified Network = iota
	// Mainnet is the main Bitcoin network.
	Mainnet
	// Regtest is the regression test network.
	Regtest
	// Testnet is the test network.
	Testnet
	// Signet is the signet network.
	Signet
)

// Values returns the values for the Network type.
func (Network) Values() []string {
	return []string{
		Unspecified.String(),
		Mainnet.String(),
		Regtest.String(),
		Testnet.String(),
		Signet.String(),
	}
}

// FromProtoNetwork converts a protobuf Network to a Network.
func FromProtoNetwork(protoNetwork pb.Network) (Network, error) {
	var network Network
	err := network.UnmarshalProto(protoNetwork)
	return network, err
}

// FromString parses a network name string and returns the corresponding Network.
func FromString(network string) (Network, error) {
	// We compare the uppercase versions because that's what's stored in the DB, even though some APIs we use
	// prvovide the lowercase versions.
	switch strings.ToUpper(network) {
	case "MAINNET":
		return Mainnet, nil
	case "REGTEST":
		return Regtest, nil
	case "TESTNET":
		return Testnet, nil
	case "SIGNET":
		return Signet, nil
	default:
		return Unspecified, sparkerrors.InternalTypeConversionError(fmt.Errorf("invalid network: %s", network))
	}
}

// String returns the uppercase string representation of the Network.
func (n Network) String() string {
	switch n {
	case Unspecified:
		return "UNSPECIFIED"
	case Regtest:
		return "REGTEST"
	case Testnet:
		return "TESTNET"
	case Signet:
		return "SIGNET"
	case Mainnet:
		return "MAINNET"
	default:
		return "UNSPECIFIED"
	}
}

// ToProtoNetwork converts a Network into a protobuf Network.
func (n Network) ToProtoNetwork() (pb.Network, error) {
	return n.MarshalProto()
}

// MarshalProto converts a Network into a spark protobuf Network.
func (n Network) MarshalProto() (pb.Network, error) {
	switch n {
	case Mainnet:
		return pb.Network_MAINNET, nil
	case Regtest:
		return pb.Network_REGTEST, nil
	case Testnet:
		return pb.Network_TESTNET, nil
	case Signet:
		return pb.Network_SIGNET, nil
	default:
		return pb.Network_UNSPECIFIED, sparkerrors.InternalTypeConversionError(fmt.Errorf("unknown network: %s", n))
	}
}

// UnmarshalProto converts a spark protobuf Network into a Network.
func (n *Network) UnmarshalProto(proto pb.Network) error {
	switch proto {
	case pb.Network_MAINNET:
		*n = Mainnet
	case pb.Network_REGTEST:
		*n = Regtest
	case pb.Network_TESTNET:
		*n = Testnet
	case pb.Network_SIGNET:
		*n = Signet
	default:
		return sparkerrors.InternalTypeConversionError(fmt.Errorf("unknown network: %v", proto))
	}
	return nil
}

// Value implements the [field.ValueScanner] interface.
func (n Network) Value() (driver.Value, error) {
	return n.String(), nil
}

// Scan implements the [field.ValueScanner] interface.
func (n *Network) Scan(src any) error {
	*n = Unspecified

	switch val := src.(type) {
	case Network:
		*n = val
	case int:
		*n = Network(val)
	case string:
		net, err := FromString(val)
		if err != nil {
			return err
		}
		*n = net
	case nil:
		return nil
	}
	return nil
}

// ToBitcoinNetworkIdentifier returns the standardized bitcoin network identifier.
func (n Network) ToBitcoinNetworkIdentifier() (uint32, error) {
	return uint32(n.Params().Net), nil
}

// Params converts a Network into its corresponding chaincfg.Params
func (n Network) Params() *chaincfg.Params {
	switch n {
	case Mainnet:
		return &chaincfg.MainNetParams
	case Regtest:
		return &chaincfg.RegressionNetParams
	case Testnet:
		return &chaincfg.TestNet3Params
	default:
		return &chaincfg.MainNetParams
	}
}
