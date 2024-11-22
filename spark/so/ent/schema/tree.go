package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
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
		field.UUID("root_id", uuid.UUID{}),
		field.Bytes("owner_identity_pubkey").NotEmpty(),
	}
}

func (Tree) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("root", Leaf.Type).
			Field("root_id").
			Unique().
			Required(),
		edge.To("leaves", Leaf.Type),
	}
}
