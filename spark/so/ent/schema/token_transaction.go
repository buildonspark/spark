package schema

import (
	"context"
	"fmt"
	"math/big"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	entgen "github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/errors"
)

type TokenTransaction struct {
	ent.Schema
}

func (TokenTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("partial_token_transaction_hash").NotEmpty(),
		field.Bytes("finalized_token_transaction_hash").NotEmpty().Unique(),
		field.Bytes("operator_signature").Optional().Unique(),
		field.Enum("status").GoType(st.TokenTransactionStatus("")).Optional(),
		field.Time("expiry_time").Optional().Immutable(),
		field.Bytes("coordinator_public_key").Optional().GoType(keys.Public{}),
		field.Time("client_created_timestamp").Optional(),
		field.Int("version").GoType(st.TokenTransactionVersion(0)).Default(int(st.TokenTransactionVersionV0)).Validate(func(v int) error {
			if !st.TokenTransactionVersion(v).IsValid() {
				return fmt.Errorf("invalid token transaction version: %d", v)
			}
			return nil
		}),
	}
}

func (TokenTransaction) Edges() []ent.Edge {
	// Token Transactions are associated with
	// a) one or more created outputs representing new withdrawable token holdings.
	// b) one or more spent outputs (for transfers) or a single mint.
	return []ent.Edge{
		edge.From("spent_output", TokenOutput.Type).
			Ref("output_spent_token_transaction"),
		edge.From("spent_output_v2", TokenOutput.Type).
			Ref("output_spent_started_token_transactions"),
		edge.From("created_output", TokenOutput.Type).
			Ref("output_created_token_transaction"),
		edge.To("mint", TokenMint.Type).
			Unique(),
		edge.To("create", TokenCreate.Type).
			Unique(),
		edge.To("payment_intent", PaymentIntent.Type).Unique(),
		edge.To("peer_signatures", TokenTransactionPeerSignature.Type),
		edge.To("spark_invoice", SparkInvoice.Type),
	}
}

func (TokenTransaction) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("finalized_token_transaction_hash"),
		index.Fields("partial_token_transaction_hash"),
		index.Fields("expiry_time", "status"),
		// Needed for query_token_transactions query
		index.Fields("update_time"),
	}
}

func (TokenTransaction) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				tm, ok := m.(*entgen.TokenTransactionMutation)
				if !ok {
					return next.Mutate(ctx, m)
				}

				result, err := next.Mutate(ctx, m)
				status, statusExists := tm.Status()
				txID, exists := tm.ID()

				if err != nil || !statusExists || !exists ||
					(status != st.TokenTransactionStatusRevealed && status != st.TokenTransactionStatusFinalized) {
					return result, err
				}

				ctx, span := tracer.Start(ctx, "TokenTransaction.BalancedTransferValidationHook")
				defer span.End()

				tx, err := tm.Client().TokenTransaction.Query().
					Where(tokentransaction.ID(txID)).
					WithSpentOutput().
					WithCreatedOutput().
					WithMint().
					WithCreate().
					Only(ctx)
				if err != nil {
					return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to fetch transaction for balance validation: %w", err))
				}

				if err := ValidateTransferTransactionBalance(tx); err != nil {
					return nil, errors.FailedPreconditionTokenRulesViolation(fmt.Errorf("transaction balance validation failed: %w", err))
				}

				return result, nil
			})
		},
	}
}

// Validates the inputs and outputs of a transfer transaction are balanced to ensure integrity of the DAG.
// If it's not a transfer transaction, it will return nil.
func ValidateTransferTransactionBalance(tx *entgen.TokenTransaction) error {
	if tx.Edges.Mint != nil || tx.Edges.Create != nil {
		return nil
	}

	inputSum := big.NewInt(0)
	for _, input := range tx.Edges.SpentOutput {
		amount := new(big.Int).SetBytes(input.TokenAmount)
		inputSum.Add(inputSum, amount)
	}

	outputSum := big.NewInt(0)
	for _, output := range tx.Edges.CreatedOutput {
		amount := new(big.Int).SetBytes(output.TokenAmount)
		outputSum.Add(outputSum, amount)
	}

	if inputSum.Cmp(outputSum) != 0 {
		return errors.FailedPreconditionTokenRulesViolation(fmt.Errorf("transaction %s in %s state: inputs (%s) must equal outputs (%s)",
			tx.ID, tx.Status, inputSum.String(), outputSum.String()))
	}

	return nil
}
