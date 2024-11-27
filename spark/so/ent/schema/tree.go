package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Tree is the schema for the trees table.
type Tree struct {
	ent.Schema
}

// Mixin is the mixin for the trees table.
func (Tree) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the trees table.
func (Tree) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("owner_identity_pubkey").NotEmpty(),
	}
}

// Edges are the edges for the trees table.
func (Tree) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("root", TreeNode.Type).
			Unique().
			Required(),
		edge.From("nodes", TreeNode.Type).Ref("tree"),
	}
}
