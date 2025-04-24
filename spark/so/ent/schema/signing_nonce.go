package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SigningNonce is the schema for the signing nonces table.
type SigningNonce struct {
	ent.Schema
}

// Mixin is the mixin for the signing nonces table.
func (SigningNonce) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes are the indexes for the signing nonces table.
func (SigningNonce) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("nonce_commitment"),
	}
}

// Fields are the fields for the signing nonces table.
func (SigningNonce) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("nonce").
			Immutable(),
		field.Bytes("nonce_commitment").
			Immutable(),
		field.Bytes("message").
			Optional(),
	}
}

// Edges are the edges for the signing nonces table.
func (SigningNonce) Edges() []ent.Edge {
	return nil
}
