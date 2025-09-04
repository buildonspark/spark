package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// PreimageRequest is the schema for the preimage request table.
type PreimageRequest struct {
	ent.Schema
}

// Mixin returns the mixin for the preimage request table.
func (PreimageRequest) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{},
	}
}

// Indexes returns the indexes for the preimage request table.
func (PreimageRequest) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("payment_hash", "receiver_identity_pubkey"),
		index.Edges("transfers"),
	}
}

// Fields returns the fields for the preimage request table.
func (PreimageRequest) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("payment_hash").
			NotEmpty(),
		field.Enum("status").
			GoType(st.PreimageRequestStatus("")),
		field.Bytes("receiver_identity_pubkey").
			Optional(),
		field.Bytes("preimage").
			Optional(),
	}
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
