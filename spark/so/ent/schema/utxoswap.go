package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	gen "github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/hook"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/entexample"
)

// UtxoSwap holds the schema definition for the UtxoSwap entity.
type UtxoSwap struct {
	ent.Schema
}

func (UtxoSwap) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (UtxoSwap) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("utxo").Unique().Annotations(entsql.IndexWhere("status != 'CANCELLED'")),
	}
}

func (UtxoSwap) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return hook.UtxoSwapFunc(func(ctx context.Context, m *gen.UtxoSwapMutation) (ent.Value, error) {
				isCreate := m.Op().Is(ent.OpCreate)
				status, hasStatus := m.Status()

				// Require utxo edge when status is being set to COMPLETED.
				if hasStatus && status == st.UtxoSwapStatusCompleted {
					if m.UtxoCleared() {
						return nil, fmt.Errorf("utxo edge is required when status is COMPLETED")
					}
					if _, utxoExists := m.UtxoID(); !utxoExists {
						if isCreate {
							return nil, fmt.Errorf("utxo edge is required when status is COMPLETED")
						}
						swapIDs, err := m.IDs(ctx)
						if err != nil {
							return nil, fmt.Errorf("failed to get swap IDs: %w", err)
						}
						for _, swapID := range swapIDs {
							hasUtxo, err := m.Client().UtxoSwap.Query().
								Where(utxoswap.ID(swapID)).
								QueryUtxo().
								Exist(ctx)
							if err != nil {
								return nil, fmt.Errorf("failed to check utxo edge: %w", err)
							}
							if !hasUtxo {
								return nil, fmt.Errorf("utxo edge is required when status is COMPLETED")
							}
						}
					}
				}

				// Prevent clearing utxo on swaps that are already COMPLETED,
				// even when status is not part of the mutation.
				if !isCreate && m.UtxoCleared() && !hasStatus {
					swapIDs, err := m.IDs(ctx)
					if err != nil {
						return nil, fmt.Errorf("failed to get swap IDs: %w", err)
					}
					for _, swapID := range swapIDs {
						existing, err := m.Client().UtxoSwap.Get(ctx, swapID)
						if err != nil {
							return nil, fmt.Errorf("failed to get swap: %w", err)
						}
						if existing.Status == st.UtxoSwapStatusCompleted {
							return nil, fmt.Errorf("utxo edge is required when status is COMPLETED")
						}
					}
				}

				return next.Mutate(ctx, m)
			})
		},
	}
}

// Fields of the UtxoSwap.
func (UtxoSwap) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").
			GoType(st.UtxoSwapStatus("")).
			Comment("Current status of the UTXO swap offer (e.g., PENDING, COMPLETED, CANCELLED).").
			Annotations(entexample.Default(st.UtxoSwapStatusCompleted)),
		field.Enum("request_type").
			GoType(st.UtxoSwapRequestType("")).
			Comment("The type of swap request (e.g., fixed amount).").
			Annotations(entexample.Default(st.UtxoSwapRequestTypeFixedAmount)),
		field.Uint64("credit_amount_sats").
			Optional().
			Comment("Amount in satoshis to be credited to the user after fees.").
			Annotations(entexample.Default(19901)),
		field.Uint64("secondary_credit_amount_sats").
			Optional().
			Nillable().
			Comment("Secondary credit amount for instant static deposit with multiple payments.").
			Annotations(entexample.Default(5000)),
		field.Uint64("max_fee_sats").
			Optional().
			Comment("Maximum fee in satoshis the user is willing to pay for this swap."),
		field.Bytes("ssp_signature").
			Optional().
			Comment("SSP's signature authorizing the swap terms.").
			Annotations(entexample.Default(
				"304402201ac2f4358518a8ce6746a295deda4f41282fb0bf1ddcc6b2566ce673bc9d5fd802200f6ee67bc5910bc779e2719926c0e98f27e8f9c9dc86e600e66d94cc0e6e0086",
			)),
		field.Bytes("ssp_identity_public_key").
			Optional().
			GoType(keys.Public{}).
			Comment("The identity public key of the SSP or user owning this swap.").
			Annotations(entexample.Default(
				"028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4",
			)),
		field.Bytes("user_signature").
			Optional().
			Comment("User's signature authorizing the SSP to claim the UTXO after fulfilling the quote.").
			Annotations(entexample.Default(
				"304502210096f00900abd8e6f969d2f4b144885899c6b761970e62335c434079109614e1580220209afc8fd2f4ccd95b64703ec004a61c4daa4646cb929e3754d2f1aad5afab22",
			)),
		field.Bytes("user_identity_public_key").
			Optional().
			GoType(keys.Public{}).
			Comment("The identity public key of the user requesting the swap.").
			Annotations(entexample.Default(
				"037f699d5b77668b847d92a3d4ad199af4d11ebc2069cf78d7694b08be0a6b381d",
			)),
		field.Bytes("coordinator_identity_public_key").
			GoType(keys.Public{}).
			Comment("The identity public key of the distributed transaction coordinator.").
			Annotations(entexample.Default(
				"03acd9a5a88db102730ff83dee69d69088cc4c9d93bbee893e90fd5051b7da9651",
			)),
		field.UUID("requested_transfer_id", uuid.UUID{}).
			Optional().
			Comment("The transfer ID requested by the user, unique across all operators.").
			Annotations(entexample.Default("019a0ef8-5794-7677-af5f-d3948d691114")),
		field.UUID("requested_secondary_transfer_id", uuid.UUID{}).
			Optional().
			Annotations(entexample.Default("019a0ef8-5794-7677-af5f-d3948d691114")).
			Comment("The transfer id that was requested by the user for the secondary transfer, a unique reference across all operators"),
		field.Bytes("spend_tx_signing_result").
			Optional().
			Comment("The result of FROST signing the UTXO spend transaction."),
		field.Time("expiry_time").
			Optional().
			Nillable().
			Comment("When this swap offer/lock expires (if applicable)."),
		field.Uint64("utxo_value_sats").
			Comment("Amount of sats for 0-conf swap matching.").
			Annotations(entexample.Default(10000)),
	}
}

// Edges of the UtxoSwap.
func (UtxoSwap) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("utxo", Utxo.Type).
			Unique(),
		edge.To("transfer", Transfer.Type).
			Unique(),
		edge.To("secondary_transfer", Transfer.Type).
			Unique().
			Comment("Secondary transfer for instant static deposit with multiple payments."),
	}
}
