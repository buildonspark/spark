package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so/entexample"
)

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
		field.Int64("height").
			Comment("The height of the most recent block processed by the chain watcher.").
			Annotations(entexample.Default(100)),
		field.Enum("network").
			GoType(btcnetwork.Unspecified).
			Comment("The bitcoin network to which this block height belongs.").
			Annotations(entexample.Default(btcnetwork.Regtest)),
		field.Bytes("block_hash").
			Optional().
			Nillable().
			MaxLen(32).
			Comment("The hash of the most recent block processed by the chain watcher. Used to detect chain reorganizations.").
			Annotations(entexample.Default(
				"00000000000000000001bcb0c9fede3f8863b077acc30e312377e6580ceb831b",
			)),
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
