package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/index"
)

// DepositAddressPartner associates a deposit address with the partner that generated it.
type DepositAddressPartner struct {
	ent.Schema
}

// Mixin is the mixin for the DepositAddressPartner table.
func (DepositAddressPartner) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the DepositAddressPartner table.
func (DepositAddressPartner) Fields() []ent.Field {
	return nil
}

// Edges are the edges for the DepositAddressPartner table.
func (DepositAddressPartner) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("partner", Partner.Type).
			Unique().
			Required().
			Comment("The partner that generated this deposit address."),
		edge.To("deposit_address", DepositAddress.Type).
			Unique().
			Required().
			Immutable().
			Comment("The deposit address associated with this partner."),
	}
}

// Indexes are the indexes for the DepositAddressPartner table.
func (DepositAddressPartner) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("deposit_address").Unique(),
		index.Edges("partner"),
	}
}
