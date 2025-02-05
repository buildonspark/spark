package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// TokenTransactionAuthorization is the schema for tracking keys required to authorize issuance and transfers.
type TokenIssuance struct {
	ent.Schema
}

func (TokenIssuance) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenIssuance) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("issuer_public_key").NotEmpty().Immutable().Unique(),
		field.Bytes("issuer_signature").NotEmpty().Immutable().Unique(),
	}
}

func (TokenIssuance) Edges() []ent.Edge {
	return []ent.Edge{
		// Maps to the token transaction receipt representing the token issuance.
		edge.To("token_transaction_receipt_issuance", TokenTransactionReceipt.Type).
			Unique(),
	}
}
