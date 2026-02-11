package schema

import (
	"entgo.io/ent"
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
			Comment("Current state of this receiver in the claim process (e.g. INITIATED, PENDING_RECEIVER_CLAIM, COMPLETED)").
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
	}
}
