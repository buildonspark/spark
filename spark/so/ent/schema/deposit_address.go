package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
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
	}
}

func (DepositAddress) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("keyshare", SigningKeyshare.Type).
			Ref("deposit_address").
			Unique(),
	}
}
