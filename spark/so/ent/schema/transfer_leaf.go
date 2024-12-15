package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TransferLeaf is the junction schema between Transfer and TreeNode.
type TransferLeaf struct {
	ent.Schema
}

// Mixin is the mixin for the TransferLeaf table.
func (TransferLeaf) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the TransferLeaf table.
func (TransferLeaf) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("secret_cipher").NotEmpty().Immutable(),
		field.Bytes("signature").NotEmpty().Immutable(),
	}
}

// Edges are the edges for the TransferLeaf table.
func (TransferLeaf) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("transfer", Transfer.Type).
			Unique().
			Required(),
		edge.To("leaf", TreeNode.Type).
			Unique().
			Required(),
	}
}

// Indexes are the indexes for the TransferLeaf table.
func (TransferLeaf) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("transfer"),
	}
}
