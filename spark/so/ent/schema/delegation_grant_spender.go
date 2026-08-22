package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// DelegationGrantSpender is one authorized spender on a delegation grant. It is
// the policy-layer authorization record the federation meters at signing time:
// multiple spenders share one delegate key path per leaf, each fenced by its own
// caps and rolling-window counter. Revoked spenders are tombstoned, never
// deleted.
type DelegationGrantSpender struct {
	ent.Schema
}

// Mixin is the mixin for the delegation grant spenders table.
func (DelegationGrantSpender) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the delegation grant spenders table.
func (DelegationGrantSpender) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("spender_identity_pubkey").
			Immutable().
			GoType(keys.Public{}).
			Comment("Identity public key of the authorized spender (e.g. a merchant).").
			Annotations(entexample.Default(
				"02ca75659458529755b77663f18282f4aa130313e098fac40deffb1208207a2ffe",
			)),
		field.Enum("status").
			GoType(st.DelegationStatus("")).
			Default(string(st.DelegationStatusActive)).
			Comment("Spender lifecycle status (ACTIVE or REVOKED). REVOKED rows are tombstones, never deleted."),
		field.Uint64("per_tx_cap_sats").
			Comment("Maximum sats a single delegated transaction by this spender may spend.").
			Annotations(entexample.Default(0)),
		field.Uint64("rolling_limit_sats").
			Comment("Maximum sats this spender may spend within rolling_window_seconds.").
			Annotations(entexample.Default(0)),
		field.Uint64("rolling_window_seconds").
			Comment("Length of the rolling metering window in seconds.").
			Annotations(entexample.Default(0)),
		field.Bool("per_tx_unlimited").
			Default(false).
			Comment("Waives per_tx_cap_sats for this spender. Owner-signed (statement v2); false always means bounded, so a lost flag fails closed."),
		field.Bool("rolling_unlimited").
			Default(false).
			Comment("Waives rolling_limit_sats for this spender. Owner-signed (statement v2); the meter still records spend within the window."),
		field.Uint64("spent_sats").
			Default(0).
			Comment("Sats spent by this spender within the current rolling window. Decremented under ForUpdate at prepare."),
		field.Time("window_start").
			Optional().
			Comment("Start of the current rolling window; null until the first delegated spend."),
		field.Uint64("version").
			Comment("Monotonic version of this spender authorization; guards unordered replays.").
			Annotations(entexample.Default(1)),
		field.Bytes("owner_signature").
			NotEmpty().
			Comment("Owner's ECDSA signature authorizing this spender (common.CreateDelegationSpenderAddStatement).").
			Annotations(entexample.Default(
				"304402207608dd0339b19f4be059b9ca48bfe17f580f887227e30451eb35f6eb5c59ec7e02201950d40ae09d7d6c2c7ede109573021ac59a65347b0512d94172758ab4a3918f",
			)),
	}
}

// Edges are the edges for the delegation grant spenders table.
func (DelegationGrantSpender) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("delegation_grant", DelegationGrant.Type).
			Ref("spenders").
			Unique().
			Required().
			Immutable().
			Comment("The grant this spender is authorized on."),
	}
}

// Indexes are the indexes for the delegation grant spenders table.
func (DelegationGrantSpender) Indexes() []ent.Index {
	return []ent.Index{
		// At most one active authorization record per (grant, spender). Revoked
		// tombstones are excluded so a spender can be re-added after removal.
		index.Fields("spender_identity_pubkey").
			Edges("delegation_grant").
			Unique().
			Annotations(entsql.IndexWhere("status = 'ACTIVE'")).
			StorageKey("delegationgrantspender_unique_active_per_grant_spender"),
		index.Fields("spender_identity_pubkey", "status"),
	}
}
