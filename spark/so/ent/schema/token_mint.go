package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// TokenTransactionAuthorization is the schema for tracking keys required to authorize issuance and transfers.
type TokenMint struct {
	ent.Schema
}

func (TokenMint) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenMint) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("issuer_public_key").NotEmpty().Immutable(),
		field.Bytes("issuer_signature").NotEmpty().Immutable().Unique(),
		field.Bytes("operator_specific_issuer_signature").Optional().Unique(),
	}
}

func (TokenMint) Edges() []ent.Edge {
	return []ent.Edge{
		// Maps to the token transaction receipt representing the token mint.
		edge.From("token_transaction_receipt", TokenTransactionReceipt.Type).
			Ref("mint"),
	}
}

func (TokenMint) Indexes() []ent.Index {
	return []ent.Index{}
}
