package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

type CooperativeExit struct {
	ent.Schema
}

// Mixin is the mixin for the CooperativeExit table.
func (CooperativeExit) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the CooperativeExit table.
func (CooperativeExit) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("exit_txid").
			Unique().
			Immutable().
			GoType(schematype.TxID{}).
			Comment("The transaction ID of the cooperative exit transaction.").
			Annotations(entexample.Default(
				"6d4924aac6832d44ef06a0056fe6f5bc51faff37fa489518d93c012d675e2556",
			)),
		field.Int64("confirmation_height").
			Optional().
			Comment("The block height at which the cooperative exit transaction was confirmed. If null, the transaction is unconfirmed."),
	}
}

// Edges are the edges for the CooperativeExit table.
func (CooperativeExit) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("transfer", Transfer.Type).
			Unique().
			Required(),
	}
}

// Indexes are the indexes for the CooperativeExit table.
func (CooperativeExit) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("transfer"),
	}
}
