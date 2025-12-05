package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
	"github.com/lightsparkdev/spark/so/frost"
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
		field.Uint("operator_index").
			Immutable().
			Annotations(entexample.Default(2)),
		field.Enum("status").
			GoType(schematype.SigningCommitmentStatus("")).
			Annotations(entexample.Default(schematype.SigningCommitmentStatusAvailable)),
		field.Bytes("nonce_commitment").
			Immutable().
			Unique().
			GoType(frost.SigningCommitment{}).
			Annotations(entexample.Default("0358372b399f94031a235ce325a9d6ac3d700af8be5fe3fcfbbbed0bb08169e4d8029f40d1454d33ec3992ed89fd89b8c7bc2cb4afae14e03b33b36f702c978afc17")),
	}
}

// Edges are the edges for the signing nonces table.
func (SigningCommitment) Edges() []ent.Edge {
	return nil
}
