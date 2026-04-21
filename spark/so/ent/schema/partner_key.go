package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys/jwt"
	"github.com/lightsparkdev/spark/so/entexample"
)

// PartnerKey holds the partner identity and its JWT public key.
// A partner is identified by a unique partner_id (the JWT "iss" claim).
// Each PartnerKey can have multiple Partner entries (one per label/sub claim).
type PartnerKey struct {
	ent.Schema
}

// Mixin is the mixin for the PartnerKey table.
func (PartnerKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the PartnerKey table.
func (PartnerKey) Fields() []ent.Field {
	return []ent.Field{
		field.String("partner_id").
			NotEmpty().
			MaxLen(255).
			Unique().
			Comment("Identifier for the partner, included as the 'iss' claim in their JWT.").
			Annotations(entexample.Default("partner-a")),
		field.String("partner_name").
			NotEmpty().
			MaxLen(255).
			Comment("Human-readable display name for the partner.").
			Annotations(entexample.Default("Partner A")),
		field.Bytes("jwt_public_key").
			GoType(jwt.Public{}).
			Unique().
			Comment("Compressed public key (34 bytes: 1-byte curve discriminator + 33-byte compressed key) used to verify partner JWTs. Supports both secp256k1 (ES256K) and P-256 (ES256).").
			Annotations(entexample.Default("0102112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")),
	}
}

// Edges are the edges for the PartnerKey table.
func (PartnerKey) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("partners", Partner.Type).
			Ref("partner_key").
			Comment("All label-specific partner entries associated with this key."),
	}
}
