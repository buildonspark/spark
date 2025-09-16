package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
)

// TokenCreate is the schema for tracking token metadata
type TokenCreate struct {
	ent.Schema
}

func (TokenCreate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		TokenMetadataMixin{},
		NotifyMixin{},
	}
}

func (TokenCreate) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("issuer_signature").NotEmpty().Optional().Unique(),
		field.Bytes("operator_specific_issuer_signature").Optional().Unique(),
		field.Bytes("creation_entity_public_key").Immutable().GoType(keys.Public{}),
		field.Uint64("wallet_provided_timestamp").Optional().Immutable().Deprecated(),
	}
}

func (TokenCreate) Edges() []ent.Edge {
	return []ent.Edge{
		// If announced on Spark, maps to the token transaction representing the token creation.
		edge.From("token_transaction", TokenTransaction.Type).
			Ref("create"),
		// If announced on L1, maps to the L1 token creation that this Spark token creation is based on.
		edge.To("l1_token_create", L1TokenCreate.Type).
			Unique(),
		edge.To("token_output", TokenOutput.Type),
		edge.To("token_freeze", TokenFreeze.Type),
	}
}
