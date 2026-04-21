package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys/jwt"
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
		field.String("partner_id").
			Optional().
			Nillable().
			MaxLen(255).
			Deprecated().
			Comment("Deprecated: use partner_key edge.").
			Annotations(entexample.Default("partner-a")),
		field.String("label").
			NotEmpty().
			MaxLen(255).
			Comment("Label identifying the partner's client or application, included as the 'sub' claim in their JWT.").
			Annotations(entexample.Default("client-1")),
		field.String("partner_name").
			Optional().
			Nillable().
			MaxLen(255).
			Deprecated().
			Comment("Deprecated: use partner_key edge.").
			Annotations(entexample.Default("Partner A")),
		field.Bytes("jwt_public_key").
			GoType(jwt.Public{}).
			Optional().
			Nillable().
			Deprecated().
			Comment("Deprecated: use partner_key edge.").
			Annotations(entexample.Default("0102112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")),
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
