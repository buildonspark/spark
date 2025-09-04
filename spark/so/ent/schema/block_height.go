package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// BlockHeight is the last scanned block height for a given network.
type BlockHeight struct {
	ent.Schema
}

// Mixin is the mixin for the Block table.
func (BlockHeight) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{},
	}
}

// Fields are the fields for the Block table.
func (BlockHeight) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("height"),
		field.Enum("network").GoType(st.Network("")),
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
