package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// L1TokenJusticeTransaction tracks justice transactions broadcast in response to invalid withdrawals.
type L1TokenJusticeTransaction struct {
	ent.Schema
}

func (L1TokenJusticeTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (L1TokenJusticeTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("justice_tx_hash").
			GoType(schematype.TxID{}).
			Immutable().
			Comment("Transaction ID of the justice transaction.").
			Annotations(entexample.Default("6afc6ebd5ce104a3d03a927e48b05ee5b9ba52ec28dea2e4b79776e2f95de2d4")),
		field.Time("broadcast_at").
			Immutable().
			Comment("The time when the justice transaction was broadcast to the network.").
			Annotations(entexample.Default(time.Unix(0, 0))),
		field.Uint64("amount_sats").
			Immutable().
			Comment("Amount in satoshis claimed by the justice transaction.").
			Annotations(entexample.Default(10000)),
		field.Uint64("tx_cost_sats").
			Immutable().
			Comment("Transaction fee paid in satoshis for broadcasting the justice transaction.").
			Annotations(entexample.Default(1000)),
	}
}

func (L1TokenJusticeTransaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("token_output", TokenOutput.Type).
			Ref("justice_tx").
			Unique().
			Required().
			Comment("The token output that was invalidly withdrawn (the spent output being double-spent)."),
		edge.From("l1_token_output_withdrawal", L1TokenOutputWithdrawal.Type).
			Ref("justice_tx").
			Unique().
			Required().
			Comment("The invalid withdrawal that triggered this justice transaction."),
	}
}
