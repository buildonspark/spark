package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// UtxoSwap holds the schema definition for the UtxoSwap entity.
type UtxoSwap struct {
	ent.Schema
}

type UtxoSwapStatus string

const (
	UtxoSwapStatusCreated   UtxoSwapStatus = "CREATED"
	UtxoSwapStatusCompleted UtxoSwapStatus = "COMPLETED"
	UtxoSwapStatusCancelled UtxoSwapStatus = "CANCELLED"
)

func (UtxoSwapStatus) Values() []string {
	return []string{
		string(UtxoSwapStatusCreated),
		string(UtxoSwapStatusCompleted),
		string(UtxoSwapStatusCancelled),
	}
}

type UtxoSwapRequestType string

const (
	UtxoSwapRequestTypeFixedAmount UtxoSwapRequestType = "FIXED_AMOUNT"
	UtxoSwapRequestTypeMaxFee      UtxoSwapRequestType = "MAX_FEE"
	UtxoSwapRequestTypeRefund      UtxoSwapRequestType = "REFUND"
)

func (UtxoSwapRequestType) Values() []string {
	return []string{
		string(UtxoSwapRequestTypeFixedAmount),
		string(UtxoSwapRequestTypeMaxFee),
		string(UtxoSwapRequestTypeRefund),
	}
}

func UtxoSwapFromProtoRequestType(requestType pb.UtxoSwapRequestType) UtxoSwapRequestType {
	switch requestType {
	case pb.UtxoSwapRequestType_Fixed:
		return UtxoSwapRequestTypeFixedAmount
	case pb.UtxoSwapRequestType_MaxFee:
		return UtxoSwapRequestTypeMaxFee
	case pb.UtxoSwapRequestType_Refund:
		return UtxoSwapRequestTypeRefund
	default:
		return UtxoSwapRequestTypeFixedAmount
	}
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
		field.Enum("status").GoType(UtxoSwapStatus("")),
		// quote
		field.Enum("request_type").GoType(UtxoSwapRequestType("")),
		field.Uint64("credit_amount_sats").Optional(),
		field.Uint64("max_fee_sats").Optional(),
		field.Bytes("ssp_signature").Optional(),
		field.Bytes("ssp_identity_public_key").Optional(),
		// authorization from a user to claim this utxo after fulfilling the quote
		field.Bytes("user_signature").Optional(),
		field.Bytes("user_identity_public_key").Optional(),
		// distributed transaction coordinator identity public key
		field.Bytes("coordinator_identity_public_key"),
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
