package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// UtxoSwap holds the schema definition for the UtxoSwap entity.
type UtxoSwap struct {
	ent.Schema
}

// Add generic fields
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
		field.Enum("status").GoType(st.UtxoSwapStatus("")),
		// quote
		field.Enum("request_type").GoType(st.UtxoSwapRequestType("")),
		field.Uint64("credit_amount_sats").Optional(),
		field.Uint64("max_fee_sats").Optional(),
		field.Bytes("ssp_signature").Optional(),
		// SspIdentityPublicKey is the owner of the utxo swap. It can be a SSP or a user.
		field.Bytes("ssp_identity_public_key").Optional(),
		// authorization from a user to claim this utxo after fulfilling the quote
		field.Bytes("user_signature").Optional(),
		field.Bytes("user_identity_public_key").Optional(),
		// distributed transaction coordinator identity public key
		field.Bytes("coordinator_identity_public_key"),
		// the transfer id that was requested by the user, a unique reference accross all operators
		field.UUID("requested_transfer_id", uuid.UUID{}).Optional(),
		// the result of frost signing the spend transaction
		field.Bytes("spend_tx_signing_result").Optional(),
	}
}

// Edges of the UtxoSwap.
func (UtxoSwap) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("utxo", Utxo.Type).
			Unique().Required().Immutable(),
		edge.To("transfer", Transfer.Type).
			Unique(),
	}
}
