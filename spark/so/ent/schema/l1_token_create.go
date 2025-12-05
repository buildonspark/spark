package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// L1TokenCreate is the schema for tracking token metadata announced on L1.
type L1TokenCreate struct {
	ent.Schema
}

func (L1TokenCreate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		TokenMetadataMixin{},
	}
}

func (L1TokenCreate) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("transaction_id").
			GoType(schematype.TxID{}).
			Immutable().
			Unique().
			Annotations(entexample.Default("26c83883d1d642dea2108725fefae1867620753d51f9539dfc2d52676bd5a4fd")),
	}
}

func (L1TokenCreate) Edges() []ent.Edge {
	return []ent.Edge{}
}
