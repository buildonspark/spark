package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type SigningNonce struct {
	ent.Schema
}

func (SigningNonce) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (SigningNonce) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("nonce_commitment"),
	}
}

func (SigningNonce) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("nonce").
			Immutable(),
		field.Bytes("nonce_commitment").
			Immutable(),
	}
}

func (SigningNonce) Edges() []ent.Edge {
	return nil
}
