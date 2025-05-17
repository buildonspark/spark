package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Transaction outputs seen confirmed on chain by chain watcher.
// Currently used in static deposit flow, but their generic structure allows
// them to be used elsewhere.
type Utxo struct {
	ent.Schema
}

// Add generic fields
func (Utxo) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields of the Utxo.
func (Utxo) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("block_height").Immutable(),
		field.Bytes("txid").NotEmpty().Immutable(),
		field.Uint32("vout").Immutable(),
		field.Uint64("amount").Immutable(),
		field.Enum("network").GoType(Network("")).Immutable(),
		field.Bytes("pk_script").Immutable(),
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
