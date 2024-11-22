package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

type LeafStatus string

const (
	LeafStatusAvailable      LeafStatus = "AVAILABLE"
	LeafStatusFrozenByIssuer LeafStatus = "FROZEN_BY_ISSUER"
	LeafStatusTransferLocked LeafStatus = "TRANSFER_LOCKED"
	LeafStatusSplitLocked    LeafStatus = "SPLIT_LOCKED"
	LeafStatusSplitted       LeafStatus = "SPLITTED"
	LeafStatusAggregated     LeafStatus = "AGGREGATED"
	LeafStatusOnChain        LeafStatus = "ON_CHAIN"
)

func (LeafStatus) Values() []string {
	return []string{
		string(LeafStatusAvailable),
		string(LeafStatusFrozenByIssuer),
		string(LeafStatusTransferLocked),
		string(LeafStatusSplitLocked),
		string(LeafStatusSplitted),
		string(LeafStatusAggregated),
		string(LeafStatusOnChain),
	}
}

type Leaf struct {
	ent.Schema
}

func (Leaf) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (Leaf) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("value_sats").Immutable(),
		field.Enum("status").GoType(LeafStatus("")),
		field.UUID("tree_id", uuid.UUID{}),
		field.UUID("parent_id", uuid.UUID{}),
		field.Bytes("verifying_pubkey").NotEmpty().Immutable(),
		field.Bytes("owner_identity_pubkey").NotEmpty(),
		field.Bytes("owner_signing_pubkey").NotEmpty(),
		field.UUID("signing_keyshare_id", uuid.UUID{}),
	}
}

func (Leaf) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tree", Tree.Type).
			Field("tree_id").
			Ref("leaves").
			Unique().
			Required(),
		edge.To("parent", Leaf.Type).
			Field("parent_id").
			Required().
			Unique(),
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Field("signing_keyshare_id").
			Unique().
			Required(),
	}
}
