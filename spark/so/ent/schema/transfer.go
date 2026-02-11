package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
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
			Immutable().
			GoType(keys.Public{}).
			Comment("The identity public key of the sender of the transfer.").
			Annotations(entexample.Default(
				"02112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3",
			)),
		field.Bytes("receiver_identity_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Comment("The identity public key of the receiver of the transfer.").
			Annotations(entexample.Default(
				"02e0b8d42c5d3b5fe4c5beb6ea796ab3bc8aaf28a3d3195407482c67e0b58228a5",
			)),
		field.Enum("network").
			Immutable().
			GoType(btcnetwork.Unspecified).
			Comment("The network on which the transfer is taking place.").
			Annotations(entexample.Default(btcnetwork.Regtest)),
		field.Uint64("total_value").
			Comment("Amount of the transfer in satoshis.").
			Annotations(entexample.Default(30)),
		field.Enum("status").
			GoType(st.TransferStatus("")).
			Comment("Current state of the transfer through its multi-step lifecycle (e.g., SENDER_INITIATED, SENDER_KEY_TWEAKED, COMPLETED, EXPIRED).").
			Annotations(entexample.Default(st.TransferStatusCompleted)),
		field.Enum("type").
			GoType(st.TransferType("")).
			Comment("Type of transfer operation (standard, preimage swap, atomic swap, etc.).").
			Annotations(entexample.Default(st.TransferTypePreimageSwap)),
		field.Time("expiry_time").
			Immutable().
			Comment("Time when the transfer expires if not completed. Expired transfers are cancelled, and their locked leaves are returned to the sender and made available for new transfers.").
			Annotations(entexample.Default(time.Unix(0, 0))),
		field.Time("completion_time").
			Optional().
			Nillable().
			Comment("Time when the transfer was successfully completed (null until completion)."),
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
		edge.To("counter_swap_transfer", Transfer.Type).Comment("For SWAP type transfer, this field references the corresponding counter transfer (type COUNTER_SWAP), which will establish this edge automatically upon creation."),
		edge.From("primary_swap_transfer", Transfer.Type).Unique().Ref("counter_swap_transfer").Comment("For counter transfers of type COUNTER_SWAP, this field references the corresponding primary transfer (type SWAP) that initiated the atomic swap. There are multiple counter transfers possible for a single primary transfer, because if a counter transfer fails the SSP will create a new one."),
		edge.From("transfer_senders", TransferSender.Type).Ref("transfer"),
		edge.From("transfer_receivers", TransferReceiver.Type).Ref("transfer"),
	}
}

// Indexes are the indexes for the tree nodes table.
func (Transfer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("sender_identity_pubkey"),
		index.Fields("receiver_identity_pubkey"),
		index.Fields("status"),
		index.Fields("update_time"),
		// TODO(mhr): This is mostly for the backfill and can probably be removed later.
		index.Fields("network"),

		// Partial indexes for cancel_expired_transfers task - optimized for each OR branch
		index.Fields("status", "expiry_time", "type").
			Annotations(
				entsql.IndexWhere("status = 'SENDER_INITIATED' AND type <> 'COUNTER_SWAP' AND expiry_time <> '1970-01-01 00:00:00+00'"),
			).
			StorageKey("idx_transfers_cancel_sender_initiated"),
		index.Fields("status", "expiry_time", "type").
			Annotations(
				entsql.IndexWhere("status = 'SENDER_KEY_TWEAK_PENDING' AND type = 'PREIMAGE_SWAP' AND expiry_time <> '1970-01-01 00:00:00+00'"),
			).
			StorageKey("idx_transfers_cancel_preimage_swap"),

		index.Fields("receiver_identity_pubkey", "status", "create_time").
			Annotations(entsql.DescColumns("create_time")).
			StorageKey("idx_transfers_recv_status_create"),

		index.Fields("sender_identity_pubkey", "status", "create_time").
			Annotations(entsql.DescColumns("create_time")).
			StorageKey("idx_transfers_sender_status_create"),

		index.Fields("spark_invoice_id").
			Unique().
			Annotations(
				entsql.IndexWhere("CAST(status AS TEXT) IN ('SENDER_KEY_TWEAK_PENDING', 'SENDER_INITIATED_COORDINATOR')"),
			).
			StorageKey("idx_transfers_spark_invoice_pending"),
		index.Fields("spark_invoice_id").
			Unique().
			Annotations(
				entsql.IndexWhere("CAST(status AS TEXT) IN ('SENDER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAK_LOCKED', 'RECEIVER_KEY_TWEAK_APPLIED', 'RECEIVER_REFUND_SIGNED', 'COMPLETED')"),
			).
			StorageKey("idx_transfers_spark_invoice_completed"),
	}
}
