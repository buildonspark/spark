package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Network is the network type.
type Network string

const (
	NetworkMainnet Network = "MAINNET"
	NetworkRegtest Network = "REGTEST"
	NetworkTestnet Network = "TESTNET"
	NetworkSignet  Network = "SIGNET"
)

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
