package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/so/entexample"
)

// Partner represents a (partner_key, label) combination for partner attribution.
// The partner identity (partner_id, name, public key) lives in PartnerKey.
type Partner struct {
	ent.Schema
}

// Mixin is the mixin for the Partner table.
func (Partner) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes are the indexes for the Partner table.
func (Partner) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("partner_key").Fields("label").Unique(),
	}
}

// Fields are the fields for the Partner table.
func (Partner) Fields() []ent.Field {
	return []ent.Field{
		field.String("label").
			NotEmpty().
			MaxLen(255).
			Comment("Label identifying the partner's client or application, included as the 'sub' claim in their JWT.").
			Annotations(entexample.Default("client-1")),
	}
}

// Edges are the edges for the Partner table.
func (Partner) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("partner_key", PartnerKey.Type).
			Unique().
			Required().
			Comment("The partner key (identity + public key) this label belongs to."),
	}
}
