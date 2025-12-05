package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

// TokenCreate is the schema for tracking token metadata
type TokenCreate struct {
	ent.Schema
}

func (TokenCreate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		TokenMetadataMixin{},
	}
}

func (TokenCreate) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("issuer_signature").
			NotEmpty().
			Optional().
			Unique().
			Annotations(entexample.Default("c4d0f7f4ed725175ea7f93e3c3864a4fe8f386c5652964b736c7ab7752c939c84d40affa0876733deb843a466c74662e82c94857324e07bcb597097034b3c949")),
		field.Bytes("operator_specific_issuer_signature").
			Optional().
			Unique(),
		field.Bytes("creation_entity_public_key").
			Immutable().
			GoType(keys.Public{}).
			Annotations(entexample.Default("0264a6f0a4f02477123875eceb43592369848081d329f3db0eba7445a4abed23b8")),
		field.Uint64("wallet_provided_timestamp").
			Optional().
			Immutable().
			Deprecated(),
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
