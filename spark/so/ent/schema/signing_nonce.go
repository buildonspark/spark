package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/so/entexample"
	"github.com/lightsparkdev/spark/so/frost"
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
			Immutable().
			GoType(frost.SigningNonce{}).
			Annotations(entexample.Default("08ce96b0ee43c64e8fe1b910bc0a97d08181c050743609ef48d544e4706e4681ce3bd729e24461f447ccc89478fed2218dbfc6a5db8e4c6f43bc5acff738c2f9")),
		field.Bytes("nonce_commitment").
			Immutable().
			GoType(frost.SigningCommitment{}).
			Annotations(entexample.Default("02b1da9d3de7774d492150db96dea151050a7c9e4459e35020d4b768c4b4044e8103f694a39e78d4804c985ff637d6e3a56052b5a122d2edd1cf75e385f6b69297dd")),
		field.Bytes("retry_fingerprint").
			Optional().
			Annotations(entexample.Default("0c2b065352d08570c7153081b57773a6fd1e592c3e697dc624d9b368aad10903")),
	}
}

// Edges are the edges for the signing nonces table.
func (SigningNonce) Edges() []ent.Edge {
	return nil
}
