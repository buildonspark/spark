package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

type TokenTransactionPeerSignature struct {
	ent.Schema
}

func (TokenTransactionPeerSignature) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenTransactionPeerSignature) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Holds the signatures for a token transaction from the peer operators. " +
			"DO NOT WRITE an operator's own signature in this table. That already exists in the TokenTransaction table."),
	}
}

func (TokenTransactionPeerSignature) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("operator_identity_public_key").
			GoType(keys.Public{}).
			Annotations(entexample.Default(
				"0350f07ffc21bfd59d31e0a7a600e2995273938444447cb9bc4c75b8a895dbb853",
			)),
		field.Bytes("signature").
			NotEmpty().
			Annotations(entexample.Default(
				"30440220187bd58858cccab1abe381454148ee545c605d9f0e9bbfea80e746ddc5b622da02203679a8259e339802e1c153631700117b0934c9ffe76e9ec3d0d17f1352652915",
			)),
	}
}

func (TokenTransactionPeerSignature) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("token_transaction", TokenTransaction.Type).
			Ref("peer_signatures").
			Unique().
			Required(),
	}
}

func (TokenTransactionPeerSignature) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("token_transaction"),
		index.Fields("operator_identity_public_key").
			Edges("token_transaction").
			Unique().
			Annotations(
				schema.Comment(
					"Ensures each operator can add at most one peer signature for a given token transaction.",
				),
			),
	}
}
