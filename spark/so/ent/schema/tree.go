package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// Tree is the schema for the trees table.
type Tree struct {
	ent.Schema
}

// Mixin is the mixin for the trees table.
func (Tree) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the trees table.
func (Tree) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("owner_identity_pubkey").
			GoType(keys.Public{}).
			Annotations(entexample.Default("028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4")),
		field.Enum("status").
			GoType(st.TreeStatus("")).
			Annotations(entexample.Default(st.TreeStatusAvailable)),
		field.Enum("network").
			GoType(btcnetwork.Unspecified).
			Annotations(entexample.Default(btcnetwork.Regtest)),
		field.Bytes("base_txid").
			GoType(st.TxID{}).
			Annotations(entexample.Default("bb736bfae9b0a47584bbdbb27606eedef1b5bb3927d692f339909863c22e27d2")),
		field.Int16("vout").
			NonNegative().
			Annotations(entexample.Default(0)),
	}
}

// Edges are the edges for the trees table.
func (Tree) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("root", TreeNode.Type).Unique(),
		edge.From("nodes", TreeNode.Type).Ref("tree"),
		edge.From("deposit_address", DepositAddress.Type).Ref("tree").Unique(),
	}
}

// Indexes are the indexes for the trees table.
func (Tree) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("network"),
		index.Fields("base_txid", "vout").Unique(),
	}
}
