package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
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
		NotifyMixin{AdditionalFields: []string{"receiver_identity_pubkey", "status"}},
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
		field.UUID("spark_invoice_id", uuid.UUID{}).
			Optional().
			Comment("Foreign key to spark_invoice"),
	}
}

// Edges are the edges for the tree nodes table.
func (Transfer) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("transfer_leaves", TransferLeaf.Type).Ref("transfer"),
		edge.To("payment_intent", PaymentIntent.Type).Unique(),
		edge.To("spark_invoice", SparkInvoice.Type).
			Unique().
			Field("spark_invoice_id").
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
		// index.Fields("spark_invoice_id").
		// 	Unique().
		// 	Annotations(
		// 		entsql.IndexWhere("status IN ('SENDER_KEY_TWEAK_PENDING', 'SENDER_INITIATED_COORDINATOR')"),
		// 	).
		// 	StorageKey("idx_transfers_spark_invoice_pending"),
		// index.Fields("spark_invoice_id").
		// 	Unique().
		// 	Annotations(
		// 		entsql.IndexWhere("status IN ('SENDER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAK_LOCKED', 'RECEIVER_KEY_TWEAK_APPLIED', 'RECEIVER_REFUND_SIGNED', 'COMPLETED')"),
		// 	).
		// 	StorageKey("idx_transfers_spark_invoice_completed"),
	}
}
