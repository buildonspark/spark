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
