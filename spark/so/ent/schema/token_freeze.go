package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// TokenFreeze is the schema for the token leafs table.
type TokenFreeze struct {
	ent.Schema
}

// Mixin is the mixin for the token leafs table.
func (TokenFreeze) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the token leafs table.
func (TokenFreeze) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").GoType(st.TokenFreezeStatus("")),
		field.Bytes("owner_public_key").NotEmpty().Immutable(),
		field.Bytes("token_public_key").Optional().Immutable(),
		field.Bytes("issuer_signature").NotEmpty().Immutable().Unique(),
		field.Uint64("wallet_provided_freeze_timestamp").Immutable(),
		field.Uint64("wallet_provided_thaw_timestamp").Optional(),
		field.UUID("token_create_id", uuid.UUID{}).Immutable(),
	}
}

// Edges are the edges for the token leafs table.
func (TokenFreeze) Edges() []ent.Edge {
	return []ent.Edge{
		// TODO LIG-7986: Make required after backfilling legacy token freezes.
		// Add immutable and required after backfill.
		edge.
			From("token_create", TokenCreate.Type).
			Ref("token_freeze").
			Unique().
			Field("token_create_id").
			Comment("Token create contains the token metadata associated with this token freeze.").
			Immutable().
			Required(),
	}
}

// Indexes are the indexes for the token leafs table.
func (TokenFreeze) Indexes() []ent.Index {
	return []ent.Index{
		// Enforce uniqueness to ensure idempotency.
		index.Fields("owner_public_key", "token_public_key", "wallet_provided_freeze_timestamp").Unique().
			StorageKey("tokenfreeze_owner_public_key_token_public_key_wallet_provided_f"),
		index.Fields("owner_public_key", "token_public_key", "wallet_provided_thaw_timestamp").Unique().
			StorageKey("tokenfreeze_owner_public_key_token_public_key_wallet_provided_t"),
		index.Fields("owner_public_key", "token_create_id", "wallet_provided_freeze_timestamp").Unique().
			StorageKey("tokenfreeze_owner_public_key_token_create_id_wallet_provided_f"),
		index.Fields("owner_public_key", "token_create_id", "wallet_provided_thaw_timestamp").Unique().
			StorageKey("tokenfreeze_owner_public_key_token_create_id_wallet_provided_t"),
	}
}
