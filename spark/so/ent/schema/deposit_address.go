package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type DepositAddress struct {
	ent.Schema
}

func (DepositAddress) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (DepositAddress) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("address"),
	}
}

func (DepositAddress) Fields() []ent.Field {
	return []ent.Field{
		field.String("address").NotEmpty(),
		field.UUID("signing_keyshare_id", uuid.UUID{}).
			Immutable(),
	}
}

func (DepositAddress) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Field("signing_keyshare_id").
			Unique().
			Required().
			Immutable(),
	}
}
