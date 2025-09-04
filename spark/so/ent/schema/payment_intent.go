package schema

import (
	"context"
	"errors"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	entgen "github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/hook"
)

type PaymentIntent struct {
	ent.Schema
}

// The ID field from the Base Mixin must be overridden by the ID decoded from the payment intent string.
func (PaymentIntent) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{},
	}
}

func (PaymentIntent) Fields() []ent.Field {
	return []ent.Field{
		field.String("payment_intent").
			NotEmpty().
			Immutable().
			Comment("The original payment intent string"),
	}
}

func (PaymentIntent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("transfer", Transfer.Type).Ref("payment_intent"),
		edge.From("token_transaction", TokenTransaction.Type).Ref("payment_intent"),
	}
}

func (PaymentIntent) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return hook.PaymentIntentFunc(func(ctx context.Context, m *entgen.PaymentIntentMutation) (ent.Value, error) {
				if len(m.TransferIDs()) == 0 && len(m.TokenTransactionIDs()) == 0 {
					return nil, errors.New("PaymentIntents must correspond to one of a Transfer or a TokenTransaction")
				}
				return next.Mutate(ctx, m)
			})
		},
	}
}
