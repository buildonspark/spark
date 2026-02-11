package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

type TransferSender struct {
	ent.Schema
}

func (TransferSender) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TransferSender) Fields() []ent.Field {
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
			Comment("The identity public key of this sender of the transfer.").
			Annotations(entexample.Default(
				"02112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3",
			)),
	}
}

func (TransferSender) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("transfer", Transfer.Type).
			Unique().
			Immutable().
			Field("transfer_id").
			Required(),
	}
}

func (TransferSender) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("identity_pubkey"),

		// Enforces that the same sender cannot be included in a transfer more than once.
		// Also serves as our "transfer" index.
		index.Fields("transfer_id", "identity_pubkey").
			Unique(),
	}
}
