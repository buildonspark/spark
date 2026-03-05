package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

// SigningKeyshareSecret holds the schema definition for the SigningKeyshareSecret entity.
type SigningKeyshareSecret struct {
	ent.Schema
}

// Mixin is the mixin for the signing keyshares table.
func (SigningKeyshareSecret) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (SigningKeyshareSecret) Indexes() []ent.Index {
	return []ent.Index{

		index.Fields("signing_keyshare_id", "version").
			Unique(),
	}
}

// Fields of the SigningKeyshareSecret.
func (SigningKeyshareSecret) Fields() []ent.Field {
	return []ent.Field{
		field.
			UUID("signing_keyshare_id", uuid.UUID{}).
			Immutable().
			Comment("The ID of the signing keyshare this secret belongs to.").
			Annotations(entexample.Default("00000000-0000-0000-0000-000000000001")),
		field.
			Int32("version").
			Comment("Version number of the secret; incremented on tweaks").
			Annotations(entexample.Default(0)),
		field.
			Bytes("secret_share").
			GoType(keys.Private{}).
			Comment("The secret share of the signing keyshare held by this SO.").
			Annotations(entexample.Default("adeab186b64a2239f15640cb43d7c57c35376f5e1c42f574671880a34a4a80ad")),
	}
}

// Edges of the SigningKeyshareSecret.
func (SigningKeyshareSecret) Edges() []ent.Edge {
	return nil
}
