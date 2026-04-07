package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// TransferPartner associates a transfer with the partner that initiated it.
type TransferPartner struct {
	ent.Schema
}

// Mixin is the mixin for the TransferPartner table.
func (TransferPartner) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the TransferPartner table.
func (TransferPartner) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("type").
			GoType(st.TransferPartnerType("")).
			Immutable().
			Comment("The type of operation: LIGHTNING_SEND, LIGHTNING_RECEIVE, or TRANSFER.").
			Annotations(entexample.Default(st.TransferPartnerTypeTransfer)),
	}
}

// Edges are the edges for the TransferPartner table.
func (TransferPartner) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("partner", Partner.Type).
			Unique().
			Required().
			Comment("The partner that initiated this transfer."),
		edge.To("transfer", Transfer.Type).
			Unique().
			Required().
			Immutable().
			Comment("The transfer associated with this partner attribution."),
	}
}

// Indexes are the indexes for the TransferPartner table.
func (TransferPartner) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("transfer").Unique(),
		index.Edges("partner"),
	}
}
