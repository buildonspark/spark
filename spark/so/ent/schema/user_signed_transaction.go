package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/lightsparkdev/spark/so/entexample"
)

// UserSignedTransaction is the schema for the user signed transaction table.
type UserSignedTransaction struct {
	ent.Schema
}

// Mixin returns the mixin for the user signed transaction table.
func (UserSignedTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes returns the indexes for the user signed transaction table.
func (UserSignedTransaction) Indexes() []ent.Index {
	return []ent.Index{}
}

// Fields returns the fields for the user signed transaction table.
func (UserSignedTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("transaction").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default(
				"03000000010d9a18670ca48b92439308cfc81336be27d524de9212ab3727d68628ab4bead80000000000400600400204000000000000002251203eba7afd7d53734e31386852f5cebf3a5adbe79495714a7326ec6601c658e08e00000000000000000451024e7300000000",
			)),
		field.Bytes("user_signature").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default(
				"6b859754fe8a89285d8d83b405a13fa9b1e93262289afd573121295b703124d2",
			)),
		field.Bytes("signing_commitments").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default(
				"0a8a010a403030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303212460a210345622188e639ffb2c81e7b1eed2c7c95a74c553e39e1cd3fa39da235a574526a12210324dd6917a392737896d11f6f82b7c1ac9030866a4539764957493fd5346882c50a8a010a403030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303312460a21033e5969a6c0dd56448c1a4f4ed183235fc142ba0586c23db0d14849c9545727b11221027427a9cf87f54147928729ac429d396279cbf43ecb59ddbde21620eda56b7dff",
			)),
		field.Bytes("user_signature_commitment").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default(
				"0a21034be0ba68fdb9cb5b44d5c75512b4bea11e9bd86892ebc4a76e833a74f8ba2d96122102b22a116652dd5dc8c1e4f69386ff48eb61a2003c9649f477aa150e44e8be03ce",
			)),
	}
}

// Edges returns the edges for the user signed transaction table.
func (UserSignedTransaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("tree_node", TreeNode.Type).
			Unique().
			Required(),
		edge.To("preimage_request", PreimageRequest.Type).
			Unique().
			Required(),
	}
}
