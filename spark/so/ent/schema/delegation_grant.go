package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// DelegationGrant is the owner-signed policy object for Spark Pull (delegated
// spending). It defines one delegate key path per leaf and the scopes/expiry/fee
// that apply to it; the authorized spenders and their limits live in the related
// DelegationGrantSpender rows. The row id (from BaseMixin) is the grant_id.
type DelegationGrant struct {
	ent.Schema
}

// Mixin is the mixin for the delegation grants table.
func (DelegationGrant) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the delegation grants table.
func (DelegationGrant) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("owner_identity_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Comment("Owner's root identity public key. Authorizes grant, spender, and revoke operations; never shared.").
			Annotations(entexample.Default(
				"02e6858515f7f886842537c752983fa3c3bc7f4b7d35057ae1e0477f637551d7e8",
			)),
		field.Enum("status").
			GoType(st.DelegationStatus("")).
			Default(string(st.DelegationStatusActive)).
			Comment("Grant lifecycle status (ACTIVE or REVOKED). REVOKED rows are tombstones, never deleted."),
		field.Enum("network").
			GoType(btcnetwork.Unspecified).
			Immutable().
			Comment("Bitcoin network this grant applies to.").
			Annotations(entexample.Default(btcnetwork.Regtest)),
		field.Time("expiry_time").
			Comment("Wall-clock time after which the grant is no longer valid.").
			Annotations(entexample.Default(time.Unix(0, 0))),
		field.Bool("scope_transfer").
			Comment("Whether the delegate path may authorize transfers.").
			Annotations(entexample.Default(true)),
		field.Bool("scope_renew").
			Comment("Whether the delegate path may authorize leaf renewals.").
			Annotations(entexample.Default(false)),
		field.Bool("scope_claim").
			Comment("Whether the delegate path may authorize offline claims.").
			Annotations(entexample.Default(false)),
		field.Uint64("fee_flat_sats").
			Comment("Flat fee (sats) a delegated settlement must pay the fee collector.").
			Annotations(entexample.Default(0)),
		field.Bytes("fee_collector_identity_pubkey").
			Optional().
			GoType(keys.Public{}).
			Comment("Identity public key that receives the flat fee; null when no fee is charged."),
		field.Uint64("version").
			Comment("Monotonic version; owner-signed re-versions supersede lower versions.").
			Annotations(entexample.Default(1)),
		field.Bytes("owner_signature").
			NotEmpty().
			Comment("Owner's ECDSA signature over the grant statement (common.CreateDelegationGrantStatement).").
			Annotations(entexample.Default(
				"304402207608dd0339b19f4be059b9ca48bfe17f580f887227e30451eb35f6eb5c59ec7e02201950d40ae09d7d6c2c7ede109573021ac59a65347b0512d94172758ab4a3918f",
			)),
	}
}

// Edges are the edges for the delegation grants table.
func (DelegationGrant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("spenders", DelegationGrantSpender.Type).
			Comment("Authorized spenders on this grant's delegate path, each independently metered."),
		edge.To("leaf_decompositions", LeafDecomposition.Type).
			Comment("Delegate-path decompositions installed on the owner's leaves under this grant."),
	}
}

// Indexes are the indexes for the delegation grants table.
func (DelegationGrant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("owner_identity_pubkey"),
	}
}
