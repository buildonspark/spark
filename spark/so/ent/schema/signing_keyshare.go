package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// SigningKeyshare holds the schema definition for the SigningKeyshare entity.
type SigningKeyshare struct {
	ent.Schema
}

// Mixin is the mixin for the signing keyshares table.
func (SigningKeyshare) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Indexes are the indexes for the signing keyshares table.
func (SigningKeyshare) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("coordinator_index"),
		// Partial index for AVAILABLE and PENDING keys only - optimized for hot path (claiming keys)
		// and confirming pending keys (marking keys as AVAILABLE).
		// This is smaller and faster than the composite index for the common case
		index.Fields("coordinator_index", "status").
			Annotations(
				entsql.IndexWhere("CAST(status AS TEXT) IN ('AVAILABLE', 'PENDING')"),
			).
			StorageKey("idx_signing_keyshares_coordinator_available_or_pending"),
	}
}

// Fields are the fields for the signing keyshares table.
func (SigningKeyshare) Fields() []ent.Field {
	return []ent.Field{
		field.
			Enum("status").
			GoType(st.SigningKeyshareStatus("")).
			Comment("The status of the signing keyshare (i.e. whether it is in use or not).").
			Annotations(entexample.Default(st.SigningCommitmentStatusUsed)),
		field.
			Bytes("secret_share").
			GoType(keys.Private{}).
			Comment("The secret share of the signing keyshare held by this SO.").
			Annotations(entexample.Default("adeab186b64a2239f15640cb43d7c57c35376f5e1c42f574671880a34a4a80ad")),
		field.
			JSON("public_shares", map[string]keys.Public{}).
			Comment("A map from SO identifier to the public key of the secret share held by that SO.").
			Annotations(entexample.Default(map[string]string{
				"0000000000000000000000000000000000000000000000000000000000000001": "02ba038e5c7daeb054eafea44d6b9cf0b0fad1b536a73652229e13183a2e0abc0e",
				"0000000000000000000000000000000000000000000000000000000000000002": "033c58b08c525b8b4536f0ab2848556aa6b77be0bbd732647e054d5b54441b0f45",
				"0000000000000000000000000000000000000000000000000000000000000003": "02ae93178db821f37fd57e8b27faf57a797d94f6b96a86474b80dcbc6f3cb086ae",
			})),
		field.
			Bytes("public_key").
			Unique().
			GoType(keys.Public{}).
			Comment("The public key of the combined secret represented by this signing keyshare.").
			Annotations(entexample.Default("034f7f0c97cb4404b28e24f9b8f32a82ef15de09ce23867c9e4fe133b9b1d860a7")),
		field.
			Int32("min_signers").
			Comment("The minimum number of signers required to produce a valid signature using this signing keyshare.").
			Annotations(entexample.Default(2)),
		field.
			Uint64("coordinator_index").
			Comment("The SO index of the coordinator that initiated the DKG round that produced this signing keyshare. " +
				"An SO can only claim a signing keyshare to mark it in-use for which it is the coordinator.",
			).
			Annotations(entexample.Default(0)),
	}
}

// Edges are the edges for the signing keyshares table.
func (SigningKeyshare) Edges() []ent.Edge {
	return nil
}
