package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EntityDkgKey represents a reserve DKG key used to identify the entire Spark Entity.
// Should only have one entry and should stay immutable for the lifetime of the entity.
type EntityDkgKey struct {
	ent.Schema
}

func (EntityDkgKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (EntityDkgKey) Fields() []ent.Field {
	return []ent.Field{
		// Key type field to ensure only one entity DKG key can exist
		field.String("key_type").
			Default("initial_entity_dkg_key").
			Immutable(),
	}
}

func (EntityDkgKey) Indexes() []ent.Index {
	return []ent.Index{
		// Enforce singleton constraint - only one entity DKG key can exist
		index.Fields("key_type").Unique(),
	}
}

func (EntityDkgKey) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Unique().
			Required().
			Immutable(),
	}
}
