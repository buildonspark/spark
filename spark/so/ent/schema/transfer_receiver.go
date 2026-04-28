package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

type TransferReceiver struct {
	ent.Schema
}

func (TransferReceiver) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{AdditionalFields: []string{"status"}},
	}
}

func (TransferReceiver) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("transfer_id", uuid.UUID{}).
			Immutable().
			Comment("Foreign key to Transfer").
			Annotations(entexample.Default(
				"cbafb67d-09dc-45ee-ade4-00df51ba2722",
			)),
		field.Bytes("identity_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Comment("The identity public key of this receiver of the transfer.").
			Annotations(entexample.Default(
				"02112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3",
			)),
		field.Enum("status").
			GoType(schematype.TransferReceiverStatus("")).
			Comment("Current state of this receiver in the claim process (e.g. INITIATED, RECEIVER_CLAIM_PENDING, RECEIVER_KEY_TWEAKED, COMPLETED)").
			Annotations(entexample.Default(schematype.TransferReceiverStatusCompleted)),
		field.Time("completion_time").
			Optional().
			Comment("The time when the transfer claim was completed."),
	}
}

func (TransferReceiver) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("transfer", Transfer.Type).
			Unique().
			Immutable().
			Field("transfer_id").
			Required(),
	}
}

func (TransferReceiver) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("identity_pubkey"),

		// Enforces that the same receiver cannot be included in a transfer more than once.
		// Also serves as our "transfer" index.
		index.Fields("transfer_id", "identity_pubkey").
			Unique(),

		// Efficiently query receivers for specific Transfers that have specific statuses
		index.Fields("transfer_id", "status"),

		// Optimizes MIMO queryTransfers ID lookup: allows Postgres to scan
		// by identity_pubkey ordered by create_time without joining to transfers.
		index.Fields("identity_pubkey", "create_time").
			Annotations(entsql.DescColumns("create_time")),

		// Partial index covering all non-terminal receiver states (INITIATED
		// + 4 stuck). Companion to idx_transferreceiver_stuck_create_time
		// below; the leading identity_pubkey column makes this the per-user
		// partial.
		//
		// **Deprecated** — being replaced by idx_transferreceiver_claim_pending_pubkey_time.
		// The new partial drops INITIATED (which was only here to support the
		// pending-receiver query path that now reads RECEIVER_CLAIM_PENDING
		// instead). This index will be dropped after the
		// INITIATED → RECEIVER_CLAIM_PENDING backfill completes.
		index.Fields("identity_pubkey", "create_time", "transfer_id").
			Annotations(
				entsql.DescColumns("create_time", "transfer_id"),
				entsql.IndexWhere("CAST(status AS TEXT) IN ('INITIATED', 'RECEIVER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAK_LOCKED', 'RECEIVER_KEY_TWEAK_APPLIED', 'RECEIVER_REFUND_SIGNED')"),
			).
			StorageKey("idx_transferreceiver_pending_pubkey_time"),

		// Partial index covering RECEIVER_CLAIM_PENDING + the 4 stuck states.
		// Drives the receiver-arm of queryPendingTransfers for multi-receiver.
		index.Fields("identity_pubkey", "create_time", "transfer_id").
			Annotations(
				entsql.DescColumns("create_time", "transfer_id"),
				entsql.IndexWhere("status IN ('RECEIVER_CLAIM_PENDING', 'RECEIVER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAK_LOCKED', 'RECEIVER_KEY_TWEAK_APPLIED', 'RECEIVER_REFUND_SIGNED')"),
			).
			StorageKey("idx_transferreceiver_claim_pending_pubkey_time"),

		// Partial index covering only the four receiver-stuck statuses,
		// keyed on (create_time DESC, transfer_id DESC) — no identity_pubkey
		// leading column. Companion to idx_transferreceiver_pending_pubkey_time
		// above; the absence of a pubkey leading column makes this the
		// time-ordered partial used for queries that scan across all users.
		index.Fields("create_time", "transfer_id").
			Annotations(
				entsql.DescColumns("create_time", "transfer_id"),
				entsql.IndexWhere("status IN ('RECEIVER_KEY_TWEAKED', 'RECEIVER_KEY_TWEAK_LOCKED', 'RECEIVER_KEY_TWEAK_APPLIED', 'RECEIVER_REFUND_SIGNED')"),
			).
			StorageKey("idx_transferreceiver_stuck_create_time"),
	}
}
