package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
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
		field.Enum("status").
			GoType(st.TokenFreezeStatus("")).
			Annotations(entexample.Default(st.TokenFreezeStatusThawed)),
		field.Bytes("owner_public_key").
			Immutable().
			GoType(keys.Public{}).
			Annotations(entexample.Default(
				"02ca75659458529755b77663f18282f4aa130313e098fac40deffb1208207a2ffe",
			)),
		field.Bytes("token_public_key").
			Optional().
			Immutable().
			GoType(keys.Public{}).
			Annotations(entexample.Default(
				"033e40d72117ee89f7bda15d2b3d779843e6721e8e4c5078c192b50fb3782de2f5",
			)),
		field.Bytes("issuer_signature").
			NotEmpty().
			Immutable().
			Unique().
			Annotations(entexample.Default(
				"304402207608dd0339b19f4be059b9ca48bfe17f580f887227e30451eb35f6eb5c59ec7e02201950d40ae09d7d6c2c7ede109573021ac59a65347b0512d94172758ab4a3918f",
			)),
		field.Uint64("wallet_provided_freeze_timestamp").
			Immutable().
			Annotations(entexample.Default(1747337980820)),
		field.Uint64("wallet_provided_thaw_timestamp").
			Optional().
			Annotations(entexample.Default(1747338083725)),
		field.UUID("token_create_id", uuid.UUID{}).
			Immutable().
			Annotations(entexample.Default("01982f4a-791d-78cd-892b-8e558d509271")),
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
