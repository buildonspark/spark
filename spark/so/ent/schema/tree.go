package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Tree struct {
	ent.Schema
}

func (Tree) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (Tree) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("owner_identity_pubkey").NotEmpty(),
	}
}

func (Tree) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("root", TreeNode.Type).
			Unique().
			Required(),
		edge.From("nodes", TreeNode.Type).Ref("tree"),
	}
}
