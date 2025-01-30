package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
)

// PreimageRequest is the schema for the preimage request table.
type PreimageRequest struct {
	ent.Schema
}

// Mixin returns the mixin for the preimage request table.
func (PreimageRequest) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes returns the indexes for the preimage request table.
func (PreimageRequest) Indexes() []ent.Index {
	return []ent.Index{}
}

// Fields returns the fields for the preimage request table.
func (PreimageRequest) Fields() []ent.Field {
	return []ent.Field{}
}

// Edges returns the edges for the preimage request table.
func (PreimageRequest) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("transactions", UserSignedTransaction.Type).
			Ref("preimage_request"),
		edge.To("preimage_shares", PreimageShare.Type).
			Unique(),
		edge.To("transfers", Transfer.Type).
			Unique(),
	}
}
