package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DepositAddress is the schema for the deposit addresses table.
type DepositAddress struct {
	ent.Schema
}

// Mixin is the mixin for the deposit addresses table.
func (DepositAddress) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes are the indexes for the deposit addresses table.
func (DepositAddress) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("address"),
	}
}

// Fields are the fields for the deposit addresses table.
func (DepositAddress) Fields() []ent.Field {
	return []ent.Field{
		field.String("address").NotEmpty().Immutable(),
		field.Bytes("owner_identity_pubkey").NotEmpty().Immutable(),
	}
}

// Edges are the edges for the deposit addresses table.
func (DepositAddress) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("signing_keyshare", SigningKeyshare.Type).
			Unique().
			Required().
			Immutable(),
	}
}
