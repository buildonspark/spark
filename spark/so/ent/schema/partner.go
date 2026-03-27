package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

// Partner holds the partner_id and its corresponding public key used to verify partner JWTs.
type Partner struct {
	ent.Schema
}

// Mixin is the mixin for the Partner table.
func (Partner) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the Partner table.
func (Partner) Fields() []ent.Field {
	return []ent.Field{
		field.String("partner_id").
			Unique().
			NotEmpty().
			MaxLen(255).
			Comment("Arbitrary string identifier that the partner includes in their JWT. Partners may use any string value.").
			Annotations(entexample.Default("partner-a")),
		field.String("partner_name").
			NotEmpty().
			MaxLen(255).
			Comment("Human-readable display name for the partner.").
			Annotations(entexample.Default("Partner A")),
		field.Bytes("jwt_public_key").
			Unique().
			GoType(keys.JwtPubKey{}).
			Comment("Compressed public key (34 bytes: 1-byte curve discriminator + 33-byte compressed key) used to verify partner JWTs. Supports both secp256k1 (ES256K) and P-256 (ES256).").
			Annotations(entexample.Default("0102112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")),
	}
}
