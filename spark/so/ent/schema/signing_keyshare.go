package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// SigningKeyshare holds the schema definition for the SigningKeyshare entity.
type SigningKeyshare struct {
	ent.Schema
}

// Mixin is the mixin for the signing keyshares table.
func (SigningKeyshare) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
		NotifyMixin{},
	}
}

// Indexes are the indexes for the signing keyshares table.
func (SigningKeyshare) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("coordinator_index"),
		index.Fields("coordinator_index").
			Annotations(
				entsql.IndexWhere("status = 'AVAILABLE'"),
			).
			StorageKey("idx_signing_keyshares_coordinator_available"),
	}
}

// Fields are the fields for the signing keyshares table.
func (SigningKeyshare) Fields() []ent.Field {
	return []ent.Field{
		field.
			Enum("status").
			GoType(st.SigningKeyshareStatus("")).
			Comment("The status of the signing keyshare (i.e. whether it is in use or not)."),
		field.
			Bytes("secret_share").
			Comment("The secret share of the signing keyshare held by this SO."),
		field.
			JSON("public_shares", map[string]keys.Public{}).
			Comment("A map from SO identifier to the public key of the secret share held by that SO."),
		field.
			Bytes("public_key").
			Unique().
			GoType(keys.Public{}).
			Comment("The public key of the combined secret represented by this signing keyshare."),
		field.
			Int32("min_signers").
			Comment("The minimum number of signers required to produce a valid signature using this signing keyshare."),
		field.
			Uint64("coordinator_index").
			Comment("The SO index of the coordinator that initiated the DKG round that produced this signing keyshare. " +
				"An SO can only claim a signing keyshare to mark it in-use for which it is the coordinator.",
			),
	}
}

// Edges are the edges for the signing keyshares table.
func (SigningKeyshare) Edges() []ent.Edge {
	return nil
}
