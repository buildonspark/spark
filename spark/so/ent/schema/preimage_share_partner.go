package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/index"
)

// PreimageSharePartner associates a preimage share with the partner that stored it.
type PreimageSharePartner struct {
	ent.Schema
}

// Mixin is the mixin for the PreimageSharePartner table.
func (PreimageSharePartner) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the PreimageSharePartner table.
func (PreimageSharePartner) Fields() []ent.Field {
	return nil
}

// Edges are the edges for the PreimageSharePartner table.
func (PreimageSharePartner) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("partner", Partner.Type).
			Unique().
			Required().
			Comment("The partner that stored this preimage share."),
		edge.To("preimage_share", PreimageShare.Type).
			Unique().
			Required().
			Immutable().
			Comment("The preimage share associated with this partner."),
	}
}

// Indexes are the indexes for the PreimageSharePartner table.
func (PreimageSharePartner) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("preimage_share").Unique(),
		index.Edges("partner"),
	}
}
