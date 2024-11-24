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
		field.String("address").NotEmpty().Immutable(),
		field.Bytes("owner_identity_pubkey").NotEmpty().Immutable(),
	}
}

func (DepositAddress) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Unique().
			Required().
			Immutable(),
	}
}
