package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/so/entexample"
)

// L1TokenOutputWithdrawal tracks individual token output withdrawals within an L1 withdrawal transaction.
type L1TokenOutputWithdrawal struct {
	ent.Schema
}

func (L1TokenOutputWithdrawal) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (L1TokenOutputWithdrawal) Fields() []ent.Field {
	return []ent.Field{
		field.Uint16("bitcoin_vout").
			Immutable().
			Comment("Output index in the L1 transaction where the withdrawn tokens were sent.").
			Annotations(entexample.Default(0)),
	}
}

func (L1TokenOutputWithdrawal) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("token_output", TokenOutput.Type).
			Ref("withdrawal").
			Unique().
			Required().
			Comment("The token output that was withdrawn."),
		edge.From("l1_withdrawal_transaction", L1WithdrawalTransaction.Type).
			Ref("withdrawals").
			Unique().
			Required().
			Comment("The L1 transaction containing this withdrawal."),
		edge.To("justice_tx", L1TokenJusticeTransaction.Type).
			Unique().
			Comment("The justice transaction if this withdrawal was invalid and punished."),
	}
}
