package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TransferLeaf is the junction schema between Transfer and TreeNode.
type TransferLeaf struct {
	ent.Schema
}

// Mixin is the mixin for the TransferLeaf table.
func (TransferLeaf) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the TransferLeaf table.
func (TransferLeaf) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("secret_cipher").Optional(),
		field.Bytes("signature").Optional(),
		field.Bytes("previous_refund_tx").NotEmpty().Immutable(),
		field.Bytes("previous_direct_refund_tx").Optional().Immutable(),
		field.Bytes("previous_direct_from_cpfp_refund_tx").Optional().Immutable(),
		field.Bytes("intermediate_refund_tx").NotEmpty(),
		field.Bytes("intermediate_direct_refund_tx").Optional(),
		field.Bytes("intermediate_direct_from_cpfp_refund_tx").Optional(),
		field.Bytes("key_tweak").Optional(),
		field.Bytes("sender_key_tweak_proof").Optional(),
		field.Bytes("receiver_key_tweak").Optional(),
	}
}

// Edges are the edges for the TransferLeaf table.
func (TransferLeaf) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("transfer", Transfer.Type).
			Unique().
			Required(),
		edge.To("leaf", TreeNode.Type).
			Unique().
			Required(),
	}
}

// Indexes are the indexes for the TransferLeaf table.
func (TransferLeaf) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("transfer"),
		index.Edges("leaf"),
	}
}
