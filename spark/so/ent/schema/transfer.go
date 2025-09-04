package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// Transfer is the schema for the transfer table.
type Transfer struct {
	ent.Schema
}

// Mixin is the mixin for the transfer table.
func (Transfer) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the tree nodes table.
func (Transfer) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("sender_identity_pubkey").
			NotEmpty().
			Immutable().
			Comment("The identity public key of the sender of the transfer."),
		field.Bytes("receiver_identity_pubkey").NotEmpty().Immutable(),
		field.Uint64("total_value"),
		field.Enum("status").GoType(st.TransferStatus("")),
		field.Enum("type").GoType(st.TransferType("")),
		field.Time("expiry_time").Immutable(),
		field.Time("completion_time").Optional().Nillable(),
	}
}

// Edges are the edges for the tree nodes table.
func (Transfer) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("transfer_leaves", TransferLeaf.Type).Ref("transfer"),
		edge.To("payment_intent", PaymentIntent.Type).Unique(),
		edge.To("spark_invoice", SparkInvoice.Type).
			Unique().
			Comment("Invoice that this transfer pays. Only set for transfers that paid an invoice."),
	}
}

// Indexes are the indexes for the tree nodes table.
func (Transfer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("sender_identity_pubkey"),
		index.Fields("receiver_identity_pubkey"),
		index.Fields("status"),
		index.Fields("update_time"),
	}
}
