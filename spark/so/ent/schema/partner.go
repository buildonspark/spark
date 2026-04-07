package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
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

// Indexes are the indexes for the Partner table.
func (Partner) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("partner_id", "label").Unique(),
	}
}

// Fields are the fields for the Partner table.
func (Partner) Fields() []ent.Field {
	return []ent.Field{
		field.String("partner_id").
			NotEmpty().
			MaxLen(255).
			Comment("Identifier for the partner, included as the 'iss' claim in their JWT.").
			Annotations(entexample.Default("partner-a")),
		field.String("label").
			NotEmpty().
			MaxLen(255).
			Comment("Label identifying the partner's client or application, included as the 'sub' claim in their JWT.").
			Annotations(entexample.Default("client-1")),
		field.String("partner_name").
			NotEmpty().
			MaxLen(255).
			Comment("Human-readable display name for the partner.").
			Annotations(entexample.Default("Partner A")),
		field.Bytes("jwt_public_key").
			GoType(keys.JwtPubKey{}).
			Comment("Compressed public key (34 bytes: 1-byte curve discriminator + 33-byte compressed key) used to verify partner JWTs. Supports both secp256k1 (ES256K) and P-256 (ES256).").
			Annotations(entexample.Default("0102112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")),
	}
}
