package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so/entexample"
)

// Utxo contains transaction outputs seen confirmed on chain by chain watcher.
// Currently used in static deposit flow, but their generic structure allows
// them to be used elsewhere.
type Utxo struct {
	ent.Schema
}

func (Utxo) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields of the Utxo.
func (Utxo) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("block_height").
			Annotations(entexample.Default(2236097)),
		field.Bytes("txid").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default(
				"2f9841624fae808464440f897d189ef3f1e14ea86922d6550c49b34d4cb6effd",
			)),
		field.Uint32("vout").
			Immutable().
			Annotations(entexample.Default(0)),
		field.Uint64("amount").
			Immutable().
			Annotations(entexample.Default(10000)),
		field.Enum("network").
			GoType(btcnetwork.Unspecified).
			Immutable().
			Annotations(entexample.Default(btcnetwork.Regtest)),
		field.Bytes("pk_script").
			Immutable().
			Annotations(entexample.Default(
				"512089f1097344ab882061ea9aee058ed84910be8e2f92429b8835af58284c0f59d6",
			)),
	}
}

// Edges of the UtxoSwap.
func (Utxo) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("deposit_address", DepositAddress.Type).
			Ref("utxo").
			Unique().Required(),
	}
}

// Indexes are the indexes for the trees table.
func (Utxo) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("network", "txid", "vout").Unique(),
	}
}
