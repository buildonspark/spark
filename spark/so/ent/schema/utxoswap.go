package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
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

// Fields of the UtxoSwap.
func (UtxoSwap) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").
			GoType(st.UtxoSwapStatus("")).
			Annotations(entexample.Default(st.UtxoSwapStatusCompleted)),
		// quote
		field.Enum("request_type").
			GoType(st.UtxoSwapRequestType("")).
			Annotations(entexample.Default(st.UtxoSwapRequestTypeFixedAmount)),
		field.Uint64("credit_amount_sats").
			Optional().
			Annotations(entexample.Default(19901)),
		field.Uint64("secondary_credit_amount_sats").
			Optional().
			Nillable().
			Comment("Secondary credit amount for instant static deposit with multiple payments.").
			Annotations(entexample.Default(5000)),
		field.Uint64("max_fee_sats").
			Optional(),
		field.Bytes("ssp_signature").
			Optional().
			Annotations(entexample.Default(
				"304402201ac2f4358518a8ce6746a295deda4f41282fb0bf1ddcc6b2566ce673bc9d5fd802200f6ee67bc5910bc779e2719926c0e98f27e8f9c9dc86e600e66d94cc0e6e0086",
			)),
		// SspIdentityPublicKey is the owner of the utxo swap. It can be a SSP or a user.
		field.Bytes("ssp_identity_public_key").
			Optional().
			GoType(keys.Public{}).
			Annotations(entexample.Default(
				"028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4",
			)),
		// authorization from a user to claim this utxo after fulfilling the quote
		field.Bytes("user_signature").
			Optional().
			Annotations(entexample.Default(
				"304502210096f00900abd8e6f969d2f4b144885899c6b761970e62335c434079109614e1580220209afc8fd2f4ccd95b64703ec004a61c4daa4646cb929e3754d2f1aad5afab22",
			)),
		field.Bytes("user_identity_public_key").
			Optional().
			GoType(keys.Public{}).
			Annotations(entexample.Default(
				"037f699d5b77668b847d92a3d4ad199af4d11ebc2069cf78d7694b08be0a6b381d",
			)),
		// distributed transaction coordinator identity public key
		field.Bytes("coordinator_identity_public_key").
			GoType(keys.Public{}).
			Annotations(entexample.Default(
				"03acd9a5a88db102730ff83dee69d69088cc4c9d93bbee893e90fd5051b7da9651",
			)),
		// the transfer id that was requested by the user, a unique reference accross all operators
		field.UUID("requested_transfer_id", uuid.UUID{}).
			Optional().
			Annotations(entexample.Default("019a0ef8-5794-7677-af5f-d3948d691114")),
		// the result of frost signing the spend transaction
		field.Bytes("spend_tx_signing_result").
			Optional(),
		field.Time("expiry_time").
			Optional().
			Nillable().
			Comment("When this swap offer/lock expires (if applicable)."),
		// TODO: (LIG-8545) Remove Nillable and Optional once we backfill the two columns below.
		// UTXO value in sats for static deposit matching.
		field.Uint64("utxo_value_sats").
			Optional().
			Nillable().
			Comment("Amount of sats for 0-conf swap matching."),
	}
}

// Edges of the UtxoSwap.
func (UtxoSwap) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("utxo", Utxo.Type).
			Unique().Required().Immutable(),
		edge.To("transfer", Transfer.Type).
			Unique(),
		edge.To("secondary_transfer", Transfer.Type).
			Unique().
			Comment("Secondary transfer for instant static deposit with multiple payments."),
	}
}
