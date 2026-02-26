package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/entexample"
)

type TokenPartialRevocationSecretShare struct {
	ent.Schema
}

func (TokenPartialRevocationSecretShare) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (TokenPartialRevocationSecretShare) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Holds the revealed revocation secret shares for a token output from the peer operators. " +
			"DO NOT WRITE an operator's own secret share in this table. This already exists in the TokenOutput table."),
	}
}

func (TokenPartialRevocationSecretShare) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("operator_identity_public_key").
			GoType(keys.Public{}).
			Comment("The identity public key of the peer operator providing this secret share.").
			Annotations(entexample.Default("02d2d103cacb1d6355efeab27637c74484e2a7459e49110c3fe885210369782e23")),
		field.Bytes("secret_share").
			GoType(keys.Private{}).
			Comment("The partial revocation secret share from a peer operator.").
			Annotations(entexample.Default("e6d2b44c26c0c1b507fab0d5e66c388c5676c109b9ee41520ceba5b52e3a2a92")),
	}
}

func (TokenPartialRevocationSecretShare) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("token_output", TokenOutput.Type).
			Ref("token_partial_revocation_secret_shares").
			Unique().
			Required(),
	}
}

func (TokenPartialRevocationSecretShare) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("token_output"),
		index.Fields("update_time", "id").
			StorageKey("token_partial_revocation_secret_shares_update_time_id_idx"),
		index.Fields("operator_identity_public_key").
			Edges("token_output").
			Unique().
			Annotations(
				schema.Comment(
					"Ensures each operator can add at most one partial revocation secret share for a given token output.",
				),
			),
	}
}
