package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

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
		field.Bytes("issuer_public_key").
			Immutable().
			GoType(keys.Public{}).
			Annotations(entexample.Default(
				"02dcfd188fc97e7a3573c3cf089cb58802d6d2b737dec1e118ba2f7aa3c73926ea",
			)),
		field.Uint64("wallet_provided_timestamp").
			Immutable().
			Annotations(entexample.Default(1761323419265)),
		field.Bytes("issuer_signature").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default(
				"225e8bac0e3b46296f894025b306756302e8febad2bb00a2a4156e623eb9f6d3eab9263bdfbdc7fa71810ea8a7376592791ad3818c4f5bb929a31de71e9022bb",
			)),
		field.Bytes("operator_specific_issuer_signature").
			Optional().
			Unique(),
		field.Bytes("token_identifier").
			Immutable().
			Optional().
			Annotations(entexample.Default(
				"f43eceedac2f690f3f583b53010b56e8f3babf37849c97186e05034b48a2eab4",
			)),
	}
}

func (TokenMint) Edges() []ent.Edge {
	return []ent.Edge{
		// Maps to the token transaction representing the token mint.
		edge.From("token_transaction", TokenTransaction.Type).Ref("mint"),
	}
}

func (TokenMint) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("issuer_public_key"),
		index.Fields("token_identifier"),
	}
}
