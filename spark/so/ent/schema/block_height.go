package schema

import (
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

// Network is the network type.
type Network string

const (
	NetworkMainnet Network = "MAINNET"
	NetworkRegtest Network = "REGTEST"
	NetworkTestnet Network = "TESTNET"
	NetworkSignet  Network = "SIGNET"
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
	}
	return pb.Network_MAINNET, fmt.Errorf("unknown network: %s", n)
}

// Values returns the values for the Network type.
func (Network) Values() []string {
	return []string{
		string(NetworkMainnet),
		string(NetworkRegtest),
		string(NetworkTestnet),
		string(NetworkSignet),
	}
}

// BlockHeight is the last scanned block height for a given network.
type BlockHeight struct {
	ent.Schema
}

// Mixin is the mixin for the Block table.
func (BlockHeight) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the Block table.
func (BlockHeight) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("height"),
		field.Enum("network").GoType(Network("")),
	}
}

// Edges are the edges for the Block table.
func (BlockHeight) Edges() []ent.Edge {
	return []ent.Edge{}
}

// Indexes are the indexes for the Block table.
func (BlockHeight) Indexes() []ent.Index {
	return []ent.Index{}
}
