package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type PaymentIntent struct {
	ent.Schema
}

// The ID field from the Base Mixin must be overridden by the ID decoded from the payment intent string.
func (PaymentIntent) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
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

// func (PaymentIntent) Hooks() []ent.Hook {
// 	return []ent.Hook{
// 		validatePaymentIntent(),
// 	}
// }

// func validatePaymentIntent() ent.Hook {
// 	return func(next ent.Mutator) ent.Mutator {
// 		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
// 			prm, ok := m.(*entgen.PaymentIntentMutation)
// 			if !ok {
// 				return nil, errors.New("unexpected mutation type")
// 			}

// 			// Check if the ID is set and not the zero-value UUID
// 			if id, existsID := prm.ID(); !existsID || id == uuid.Nil {
// 				return nil, errors.New("the ID field must be set and not be the zero-value UUID")
// 			}

// 			// Check if either TransferID or TokenTransactionID is set
// 			if _, existsTransfer := prm.TransferID(); !existsTransfer {
// 				if _, existsTokenTransaction := prm.TokenTransactionID(); !existsTokenTransaction {
// 					return nil, errors.New("a PaymentIntent must correspond to either a Transfer or a TokenTransaction")
// 				}
// 			}

// 			return next.Mutate(ctx, m)
// 		})
// 	}
// }
