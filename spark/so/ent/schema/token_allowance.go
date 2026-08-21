package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// TokenAllowance is an owner-signed grant letting a spender move the owner's
// token outputs within federation-enforced limits.
type TokenAllowance struct {
	ent.Schema
}

// Mixin is the mixin for the token allowances table.
func (TokenAllowance) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the token allowances table.
func (TokenAllowance) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("allowance_id", uuid.UUID{}).
			Immutable().
			Unique().
			Comment("Client-generated identifier for the allowance, shared across all SOs.").
			Annotations(entexample.Default("019a0ef8-5794-7677-af5f-d3948d691114")),
		field.Enum("status").
			GoType(st.TokenAllowanceStatus("")).
			Default(string(st.TokenAllowanceStatusActive)).
			Comment("Current lifecycle status of the allowance (ACTIVE, REVOKED, EXHAUSTED, EXPIRED)."),
		field.Bytes("owner_public_key").
			Immutable().
			GoType(keys.Public{}).
			Comment("The public key of the token owner that signed this allowance.").
			Annotations(entexample.Default(
				"02ca75659458529755b77663f18282f4aa130313e098fac40deffb1208207a2ffe",
			)),
		field.Bytes("spender_public_key").
			Immutable().
			GoType(keys.Public{}).
			Comment("The public key authorized to spend under this allowance.").
			Annotations(entexample.Default(
				"033e40d72117ee89f7bda15d2b3d779843e6721e8e4c5078c192b50fb3782de2f5",
			)),
		field.Bytes("token_identifier").
			Immutable().
			Comment("The identifier of the token this allowance applies to.").
			Annotations(entexample.Default(
				"3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451",
			)),
		field.UUID("token_create_id", uuid.UUID{}).
			Immutable().
			Comment("Foreign key referencing the associated TokenCreate record.").
			Annotations(entexample.Default("01982f4a-791d-78cd-892b-8e558d509271")),
		field.Bytes("per_transaction_cap").
			Immutable().
			Comment("Maximum value a single delegated transaction may move, as a uint128 big-endian.").
			Annotations(entexample.Default("00000000000000000000000000002710")),
		field.Bytes("total_limit").
			Immutable().
			Comment("Cumulative value the spender may move over the allowance lifetime, as a uint128 big-endian.").
			Annotations(entexample.Default("000000000000000000000000000186a0")),
		field.Bool("per_transaction_unlimited").
			Optional().
			Default(false).
			Immutable().
			Comment("Owner-signed waiver of the per-transaction ceiling (payload v2+). NULL/false means bounded (fail-closed)."),
		field.Bool("total_unlimited").
			Optional().
			Default(false).
			Immutable().
			Comment("Owner-signed waiver of the lifetime total ceiling (payload v2+); spent_amount still meters. NULL/false means bounded (fail-closed)."),
		field.Bytes("spent_amount").
			Default(make([]byte, 16)).
			Comment("Value already spent against this allowance, as a uint128 big-endian."),
		field.JSON("recipient_allowlist", [][]byte{}).
			Optional().
			Immutable().
			Comment("Recipient public keys the spender may pay; empty permits any recipient."),
		field.Time("expiry_time").
			Immutable().
			Comment("Wall-clock time after which the allowance can no longer authorize spends.").
			Annotations(entexample.Default(time.Unix(0, 0))),
		field.Enum("network").
			GoType(btcnetwork.Unspecified).
			Immutable().
			Comment("The Bitcoin network this allowance operates on.").
			Annotations(entexample.Default(btcnetwork.Regtest)),
		field.Bytes("owner_signature").
			NotEmpty().
			Immutable().
			Unique().
			Comment("The owner's signature over the allowance statement hash.").
			Annotations(entexample.Default(
				"304402207608dd0339b19f4be059b9ca48bfe17f580f887227e30451eb35f6eb5c59ec7e02201950d40ae09d7d6c2c7ede109573021ac59a65347b0512d94172758ab4a3918f",
			)),
		field.Bytes("statement_hash").
			Immutable().
			Comment("The canonical statement hash the owner signed to authorize this allowance.").
			Annotations(entexample.Default(
				"a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
			)),
		field.Uint64("version").
			Immutable().
			Comment("Monotonic version of the allowance payload the owner signed.").
			Annotations(entexample.Default(1)),
		field.Uint64("owner_provided_timestamp").
			Immutable().
			Comment("Wallet-provided timestamp when the allowance was created, in milliseconds.").
			Annotations(entexample.Default(1747337980820)),
		field.UUID("flow_execution_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Consensus flow that prepared this allowance. Commit clears the stamp; rollback is scoped to it so one execution cannot delete another's grant."),
		field.Uint64("owner_provided_revoke_timestamp").
			Optional().
			Comment("Wallet-provided timestamp when the allowance was revoked, in milliseconds."),
		field.Bytes("revoke_signature").
			Optional().
			Comment("The owner's signature over the revoke statement hash, present once revoked."),
		field.Uint64("revoke_version").
			Optional().
			Comment("Version of the revoke payload the owner signed; persisted so the tombstoned grant can be served back with a verifiable revoke proof."),
	}
}

// Edges are the edges for the token allowances table.
func (TokenAllowance) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("token_create", TokenCreate.Type).
			Ref("token_allowance").
			Field("token_create_id").
			Unique().
			Immutable().
			Required().
			Comment("Token create contains the token metadata associated with this allowance."),
		edge.To("token_allowance_spend", TokenAllowanceSpend.Type).
			Comment("Metered spends recorded against this allowance."),
	}
}

// Indexes are the indexes for the token allowances table.
func (TokenAllowance) Indexes() []ent.Index {
	return []ent.Index{
		// Enforce at most one active allowance per (owner, spender, token).
		index.Fields("owner_public_key", "spender_public_key", "token_create_id").Unique().
			Annotations(entsql.IndexWhere("status = 'ACTIVE'")).
			StorageKey("tokenallowance_unique_active_grant"),
		// Index for efficient spender-scoped allowance lookups.
		index.Fields("spender_public_key", "status").
			StorageKey("tokenallowance_spender_public_key_status"),
		// Index for efficient owner-scoped allowance lookups, including inactive grants.
		index.Fields("owner_public_key", "status").
			StorageKey("tokenallowance_owner_public_key_status"),
	}
}
