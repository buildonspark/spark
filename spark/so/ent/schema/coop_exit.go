package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type CooperativeExit struct {
	ent.Schema
}

// Mixin is the mixin for the CooperativeExit table.
func (CooperativeExit) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{},
	}
}

// Fields are the fields for the CooperativeExit table.
func (CooperativeExit) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("exit_txid").Unique().Immutable(),
		field.Int64("confirmation_height").Optional(),
	}
}

// Edges are the edges for the CooperativeExit table.
func (CooperativeExit) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("transfer", Transfer.Type).
			Unique().
			Required(),
	}
}

// Indexes are the indexes for the CooperativeExit table.
func (CooperativeExit) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("transfer"),
	}
}
