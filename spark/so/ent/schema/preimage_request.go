package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
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
	return []ent.Index{
		index.Fields("payment_hash", "receiver_identity_pubkey").
			Unique().
			Annotations(entsql.IndexWhere("status != 'RETURNED'")),
		index.Fields("sender_identity_pubkey"),
		index.Edges("transfers"),
	}
}

// Fields returns the fields for the preimage request table.
func (PreimageRequest) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("payment_hash").
			NotEmpty().
			Annotations(entexample.Default("24dc566683c284f01f5f35be83d2946ea315e7986ef435fc9860633de01cda3c")),
		field.Enum("status").
			GoType(st.PreimageRequestStatus("")).
			Annotations(entexample.Default(st.PreimageRequestStatusPreimageShared)),
		field.Bytes("receiver_identity_pubkey").
			Optional().
			GoType(keys.Public{}).
			Annotations(entexample.Default("02e0b8d42c5d3b5fe4c5beb6ea796ab3bc8aaf28a3d3195407482c67e0b58228a5")),
		field.Bytes("preimage").
			Optional().
			Annotations(entexample.Default("9d8e6f8789f9406b79629e0ad753fcb9e5f8b70c53b660f0a73274b78b7905d3")),
		field.Bytes("sender_identity_pubkey").
			Optional().
			GoType(keys.Public{}).
			Annotations(entexample.Default("02112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")),
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
