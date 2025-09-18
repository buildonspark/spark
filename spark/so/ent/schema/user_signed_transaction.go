package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// UserSignedTransaction is the schema for the user signed transaction table.
type UserSignedTransaction struct {
	ent.Schema
}

// Mixin returns the mixin for the user signed transaction table.
func (UserSignedTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes returns the indexes for the user signed transaction table.
func (UserSignedTransaction) Indexes() []ent.Index {
	return []ent.Index{}
}

// Fields returns the fields for the user signed transaction table.
func (UserSignedTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("transaction").NotEmpty().Immutable(),
		field.Bytes("user_signature").NotEmpty().Immutable(),
		field.Bytes("signing_commitments").NotEmpty().Immutable(),
		field.Bytes("user_signature_commitment").NotEmpty().Immutable(),
	}
}

// Edges returns the edges for the user signed transaction table.
func (UserSignedTransaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("tree_node", TreeNode.Type).
			Unique().
			Required(),
		edge.To("preimage_request", PreimageRequest.Type).
			Unique().
			Required(),
	}
}
