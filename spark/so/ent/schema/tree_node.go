package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TreeNodeStatus string

const (
	TreeNodeStatusAvailable      TreeNodeStatus = "AVAILABLE"
	TreeNodeStatusFrozenByIssuer TreeNodeStatus = "FROZEN_BY_ISSUER"
	TreeNodeStatusTransferLocked TreeNodeStatus = "TRANSFER_LOCKED"
	TreeNodeStatusSplitLocked    TreeNodeStatus = "SPLIT_LOCKED"
	TreeNodeStatusSplitted       TreeNodeStatus = "SPLITTED"
	TreeNodeStatusAggregated     TreeNodeStatus = "AGGREGATED"
	TreeNodeStatusOnChain        TreeNodeStatus = "ON_CHAIN"
)

func (TreeNodeStatus) Values() []string {
	return []string{
		string(TreeNodeStatusAvailable),
		string(TreeNodeStatusFrozenByIssuer),
		string(TreeNodeStatusTransferLocked),
		string(TreeNodeStatusSplitLocked),
		string(TreeNodeStatusSplitted),
		string(TreeNodeStatusAggregated),
		string(TreeNodeStatusOnChain),
	}
}

type TreeNode struct {
	ent.Schema
}

func (TreeNode) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TreeNode) Fields() []ent.Field {
	return []ent.Field{
		field.Uint64("value_sats").Immutable(),
		field.Enum("status").GoType(TreeNodeStatus("")),
		field.Bytes("verifying_pubkey").NotEmpty().Immutable(),
		field.Bytes("owner_identity_pubkey").NotEmpty(),
		field.Bytes("owner_signing_pubkey").NotEmpty(),
	}
}

func (TreeNode) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("tree", Tree.Type).
			Unique().
			Required(),
		edge.To("parent", TreeNode.Type).
			Unique(),
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Unique().
			Required(),
		edge.From("children", TreeNode.Type).Ref("parent"),
	}
}

func (TreeNode) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("parent"),
		index.Edges("tree"),
		index.Fields("owner_identity_pubkey"),
	}
}
