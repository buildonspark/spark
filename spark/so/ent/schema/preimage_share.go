package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

// PreimageShare is the schema for the preimage shares table.
type PreimageShare struct {
	ent.Schema
}

// Mixin returns the mixin for the preimage shares table.
func (PreimageShare) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes returns the indexes for the preimage shares table.
func (PreimageShare) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("payment_hash"),
	}
}

// Fields returns the fields for the preimage shares table.
func (PreimageShare) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("payment_hash").
			NotEmpty().
			Immutable().
			Unique().
			Annotations(entexample.Default("652c98bae0df25fbd5d8ce46a935227b9feab2498155b20a033b31cc7e5edc19")),
		field.Bytes("preimage_share").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default("ff06a1292cc3b832c3a2c3444d1f68946583a12fcb323fcb100e332e84fcb824")),
		field.Int32("threshold").
			Immutable().
			Annotations(entexample.Default(2)),
		field.Bytes("owner_identity_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Annotations(entexample.Default("02112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")),
		field.String("invoice_string").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default("lnbc150n1p500ftzpp5v5kf3whqmujlh4wceer2jdfz0w074vjfs92myzsr8vcuclj7msvssp57pfy9xhj7ma00tzwwptwxse4wtn7yqffyy85t6tnwtf282px8w3sxqyz5vqnp4q0p92sfan5vj2a4f8q3gsfsy8qp60maeuxz858c5x0hvt5u0p0h9jrzjqtqd37k2ya0pv8pqeyjs4lklcexjyw600g9qqp62r4j0ph8fcmlfwqqqqzfv7u6g85qqqqqqqqqqthqq9qcqzpgdqu2dcxzunt95lyymrfde4jqar9wd6q9qyyssqffr3ydahx24w70gu69p32egksuc233eq4cxwu0sqpldh63rt5r9qf7ru2eja23yh9xhr0cmk7093wcn7ned5ppt6dxat8zfnpwpmwpspxsr475")),
	}
}

// Edges returns the edges for the preimage shares table.
func (PreimageShare) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("preimage_request", PreimageRequest.Type).
			Ref("preimage_shares").
			Unique(),
	}
}
