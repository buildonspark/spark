package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// SigningCommitment is the schema for the signing commitments table.
type SigningCommitment struct {
	ent.Schema
}

// Mixin is the mixin for the signing commitments table.
func (SigningCommitment) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes are the indexes for the signing nonces table.
func (SigningCommitment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("operator_index", "status"),
	}
}

// Fields are the fields for the signing nonces table.
func (SigningCommitment) Fields() []ent.Field {
	return []ent.Field{
		field.Uint("operator_index").Immutable(),
		field.Enum("status").
			GoType(schematype.SigningCommitmentStatus("")),
		field.Bytes("nonce_commitment").
			Immutable().Unique(),
	}
}

// Edges are the edges for the signing nonces table.
func (SigningCommitment) Edges() []ent.Edge {
	return nil
}
