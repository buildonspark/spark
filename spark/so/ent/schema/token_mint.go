package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TokenTransactionAuthorization is the schema for tracking keys required to authorize issuance and transfers.
type TokenMint struct {
	ent.Schema
}

func (TokenMint) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{},
	}
}

func (TokenMint) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("issuer_public_key").NotEmpty().Immutable(),
		field.Uint64("wallet_provided_timestamp").Immutable(),
		field.Bytes("issuer_signature").NotEmpty().Immutable(),
		field.Bytes("operator_specific_issuer_signature").Optional().Unique(),
		field.Bytes("token_identifier").Immutable().Optional(),
	}
}

func (TokenMint) Edges() []ent.Edge {
	return []ent.Edge{
		// Maps to the token transaction representing the token mint.
		edge.From("token_transaction", TokenTransaction.Type).
			Ref("mint"),
	}
}

func (TokenMint) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("issuer_public_key"),
		index.Fields("token_identifier"),
	}
}
