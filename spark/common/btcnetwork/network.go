package btcnetwork

import (
	"errors"
	"fmt"

	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
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

// FromSchemaNetwork converts an Ent schema Network to a Network.
func FromSchemaNetwork(schemaNetwork st.Network) (Network, error) {
	switch schemaNetwork {
	case st.NetworkMainnet:
		return Mainnet, nil
	case st.NetworkRegtest:
		return Regtest, nil
	case st.NetworkTestnet:
		return Testnet, nil
	case st.NetworkSignet:
		return Signet, nil
	default:
		return Unspecified, sparkerrors.InternalTypeConversionError(errors.New("invalid network"))
	}
}

// FromString parses a network name string and returns the corresponding Network.
func FromString(network string) (Network, error) {
	switch network {
	case "mainnet":
		return Mainnet, nil
	case "regtest":
		return Regtest, nil
	case "testnet":
		return Testnet, nil
	case "signet":
		return Signet, nil
	default:
		return Unspecified, sparkerrors.InternalTypeConversionError(fmt.Errorf("invalid network: %s", network))
	}
}

// String returns the lowercase string representation of the Network.
func (n Network) String() string {
	switch n {
	case Unspecified:
		return "unspecified"
	case Regtest:
		return "regtest"
	case Testnet:
		return "testnet"
	case Signet:
		return "signet"
	case Mainnet:
		return "mainnet"
	default:
		return "mainnet"
	}
}

// ToSchemaNetwork converts a Network into an Ent schema Network.
func (n Network) ToSchemaNetwork() (st.Network, error) {
	switch n {
	case Mainnet:
		return st.NetworkMainnet, nil
	case Regtest:
		return st.NetworkRegtest, nil
	case Testnet:
		return st.NetworkTestnet, nil
	case Signet:
		return st.NetworkSignet, nil
	default:
		return st.NetworkUnspecified, sparkerrors.InternalTypeConversionError(errors.New("invalid network"))
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

// ToBitcoinNetworkIdentifier returns the standardized bitcoin network identifier.
func (n Network) ToBitcoinNetworkIdentifier() (uint32, error) {
	params := n.Params()
	return uint32(params.Net), nil
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
