package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// L1TokenCreate is the schema for tracking token metadata announced on L1.
type L1TokenCreate struct {
	ent.Schema
}

func (L1TokenCreate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		TokenMetadataMixin{},
	}
}

func (L1TokenCreate) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("transaction_id").NotEmpty().Immutable().Unique(),
	}
}

func (L1TokenCreate) Edges() []ent.Edge {
	return []ent.Edge{}
}
