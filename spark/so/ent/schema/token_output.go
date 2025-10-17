package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	entgen "github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/errors"
)

type TokenOutput struct {
	ent.Schema
}

func (TokenOutput) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenOutput) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").GoType(st.TokenOutputStatus("")),
		field.Bytes("owner_public_key").Immutable().GoType(keys.Public{}),
		field.Uint64("withdraw_bond_sats").Immutable(),
		field.Uint64("withdraw_relative_block_locktime").Immutable(),
		field.Bytes("withdraw_revocation_commitment").Immutable(),
		field.Bytes("token_public_key").Immutable().Optional().GoType(keys.Public{}),
		field.Bytes("token_amount").NotEmpty().Immutable(),
		field.Int32("created_transaction_output_vout").Immutable(),
		field.Bytes("spent_ownership_signature").Optional(),
		field.Bytes("spent_operator_specific_ownership_signature").Optional(),
		field.Int32("spent_transaction_input_vout").Optional(),
		field.Bytes("spent_revocation_secret").Optional().GoType(keys.Private{}),
		field.Bytes("confirmed_withdraw_block_hash").Optional(),
		field.Enum("network").GoType(st.Network("")).Optional(),
		field.Bytes("token_identifier").Immutable(),
		field.UUID("token_create_id", uuid.UUID{}).Immutable(),
	}
}

func (TokenOutput) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("revocation_keyshare", SigningKeyshare.Type).
			Unique().
			Required().
			Immutable().
			Comment("The signing keyshare used to derive the revocation secret for this output."),
		edge.To("output_created_token_transaction", TokenTransaction.Type).
			Unique().
			Comment("The token transaction that created this output."),
		// This relation maps the most recent transaction attempting to spend this output.
		// It is not necessarily finalized.
		edge.To("output_spent_token_transaction", TokenTransaction.Type).
			Unique().
			Comment("The most recent token transaction attempting to spend this output. Not necessarily finalized."),
		// This relation maps all transaction attempting to spend this output.
		// No more than one of them should have been finalized.
		edge.To("output_spent_started_token_transactions", TokenTransaction.Type).
			Comment("All token transactions that attempted to spend this output. At most one will finalize."),
		edge.To("token_partial_revocation_secret_shares", TokenPartialRevocationSecretShare.Type).
			Comment("The partial revocation secret shares gathered from each SO for this token output."),
		edge.
			From("token_create", TokenCreate.Type).
			Ref("token_output").
			Immutable().
			Unique().
			Required().
			Field("token_create_id").
			Comment("Token create contains the token metadata associated with this output."),
	}
}

func (TokenOutput) Indexes() []ent.Index {
	return []ent.Index{
		// Optimized for GetOwnedTokenOutputs query
		index.Fields("owner_public_key", "status", "network"),
		index.Fields("token_identifier", "status"),
		// Enables quick unmarking of withdrawn outputs in response to block reorgs.
		index.Fields("confirmed_withdraw_block_hash"),
		// Optimize pre-emption queries by indexing the spent transaction relationship
		index.Edges("output_spent_token_transaction"),
		// For finalizing token transactions
		index.Edges("output_created_token_transaction"),
		index.Edges("output_created_token_transaction").Fields("created_transaction_output_vout").Unique(),
		index.Fields("token_create_id"),
	}
}

func (TokenOutput) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			// Validates that any REVEALED or FINALIZED token transfer transactions that are or were tied
			// to this output have balanced inputs and outputs to ensure outputs are not double spent.
			// This is a data integrity rule but the business logic should also check this.
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				om, ok := m.(*entgen.TokenOutputMutation)
				if !ok {
					return next.Mutate(ctx, m)
				}

				ctx, span := tracer.Start(ctx, "TokenOutput.BalancedTransferValidationHook_PreMutation")
				oldTxIDs, err := getOldTransactionIDs(ctx, om)
				span.End()
				if err != nil {
					return nil, err
				}

				result, err := next.Mutate(ctx, m)
				if err != nil {
					return result, err
				}

				ctx, span = tracer.Start(ctx, "TokenOutput.BalancedTransferValidationHook_PostMutation")
				defer span.End()

				if err := validateOutputTransactionReassignments(ctx, om, oldTxIDs); err != nil {
					return nil, err
				}

				return result, nil
			})
		},
	}
}

func getOldTransactionIDs(ctx context.Context, m *entgen.TokenOutputMutation) (map[uuid.UUID]struct{}, error) {
	if !m.Op().Is(ent.OpUpdate | ent.OpUpdateOne) {
		return nil, nil
	}

	outputID, exists := m.ID()
	if !exists {
		return nil, nil
	}

	createdTxChanged := m.OutputCreatedTokenTransactionCleared() || len(m.OutputCreatedTokenTransactionIDs()) > 0
	spentTxChanged := m.OutputSpentTokenTransactionCleared() || len(m.OutputSpentTokenTransactionIDs()) > 0

	if !createdTxChanged && !spentTxChanged {
		return nil, nil
	}

	existingOutput, err := m.Client().TokenOutput.Query().
		Where(tokenoutput.ID(outputID)).
		WithOutputCreatedTokenTransaction(func(q *entgen.TokenTransactionQuery) {
			q.Select(tokentransaction.FieldID, tokentransaction.FieldStatus)
		}).
		WithOutputSpentTokenTransaction(func(q *entgen.TokenTransactionQuery) {
			q.Select(tokentransaction.FieldID, tokentransaction.FieldStatus)
		}).
		Only(ctx)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to fetch existing output: %w", err))
	}

	oldTxIDs := make(map[uuid.UUID]struct{})
	if createdTxChanged && existingOutput.Edges.OutputCreatedTokenTransaction != nil {
		oldTxIDs[existingOutput.Edges.OutputCreatedTokenTransaction.ID] = struct{}{}
	}

	if spentTxChanged && existingOutput.Edges.OutputSpentTokenTransaction != nil {
		oldTxIDs[existingOutput.Edges.OutputSpentTokenTransaction.ID] = struct{}{}
	}

	return oldTxIDs, nil
}

func validateOutputTransactionReassignments(ctx context.Context, m *entgen.TokenOutputMutation, oldTxIDs map[uuid.UUID]struct{}) error {
	newCreatedTxIDs := m.OutputCreatedTokenTransactionIDs()
	newSpentTxIDs := m.OutputSpentTokenTransactionIDs()

	// Calculate total number of transactions to check and early exit if none
	expectedSize := len(oldTxIDs) + len(newCreatedTxIDs) + len(newSpentTxIDs)
	if expectedSize == 0 {
		return nil
	}

	// Pre-allocate map with expected capacity to avoid reallocation
	txIDsToCheck := make(map[uuid.UUID]struct{}, expectedSize)

	// Add old transaction IDs (these now have the output removed)
	for txID := range oldTxIDs {
		txIDsToCheck[txID] = struct{}{}
	}

	// Add new transaction IDs (these now have the output added)
	for _, txID := range newCreatedTxIDs {
		txIDsToCheck[txID] = struct{}{}
	}

	for _, txID := range newSpentTxIDs {
		txIDsToCheck[txID] = struct{}{}
	}

	txIDs := make([]uuid.UUID, 0, len(txIDsToCheck))
	for txID := range txIDsToCheck {
		txIDs = append(txIDs, txID)
	}

	txs, err := m.Client().TokenTransaction.Query().
		Where(
			tokentransaction.IDIn(txIDs...),
			tokentransaction.StatusIn(
				st.TokenTransactionStatusRevealed,
				st.TokenTransactionStatusFinalized,
			),
			tokentransaction.Not(tokentransaction.Or(tokentransaction.HasMint(), tokentransaction.HasCreate())),
		).
		WithSpentOutput().
		WithCreatedOutput().
		All(ctx)
	if err != nil {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to fetch affected transactions: %w", err))
	}

	for _, tx := range txs {
		if err := ValidateTransferTransactionBalance(tx); err != nil {
			return errors.FailedPreconditionTokenRulesViolation(fmt.Errorf("output reassignment would violate balance constraint: %w", err))
		}
	}

	return nil
}
