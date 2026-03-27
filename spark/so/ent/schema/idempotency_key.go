package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/so/entexample"
)

type IdempotencyKey struct {
	ent.Schema
}

func (IdempotencyKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the idempotency_keys table.
func (IdempotencyKey) Fields() []ent.Field {
	return []ent.Field{
		field.String("idempotency_key").
			NotEmpty().
			Immutable().
			Comment("Client-provided idempotency key for deduplication. Multiple requests with the same key return the same response.").
			Annotations(entexample.Default("my_super_cool_idempotency_key_1337")),
		field.String("method_name").
			NotEmpty().
			Immutable().
			Comment("Method name used for the API call.").
			Annotations(entexample.Default("/spark.SparkService/start_transfer_v2")),

		// This ideally would be keys.Public{}, but we can't support that for several (cascading) reasons:
		// 1. keys.Public{} serializes an empty key as NULL, which by default, PSQL ignores NULL in unique indexes
		//      (the whole point of adding this is to have a unique index on key, methd, and identity_public_key)
		// 2. BUT, we can fix this by using `NULLS NOT DISTINCT`` in the index, however, ent doesn't support that
		// 3. BUT, we can manually write the psql
		// 4. BUT, Atlas doesn't support parsing `NULLS NOT DISTINCT` when introspecting the schema, which causes
		//      issues for our CI when we run `atlas diff` to check if our ent schema is in sync with the database
		// As a result, we're going back to using []byte for the public key
		field.Bytes("identity_public_key").
			Default([]byte{}).
			Immutable().
			Annotations(entsql.DefaultExprs(map[string]string{
				dialect.Postgres: "''::bytea",
				dialect.SQLite:   "X''",
			})).
			Comment("Compressed public key of the authenticated user who created this idempotency record. Empty for internal SO-to-SO calls."),
		field.JSON("response", json.RawMessage{}).
			Optional().
			Comment("JSON-Marshalled proto response to return for subsequent requests with the same idempotency key. A NULL value indicates we're not done processing the request."),
	}
}

func (IdempotencyKey) Edges() []ent.Edge {
	return []ent.Edge{}
}

func (IdempotencyKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idempotency_key", "method_name", "identity_public_key").
			Unique().
			StorageKey("idempotency_keys_key_method_identity"),
		index.Fields("create_time").
			StorageKey("idempotency_keys_create_time"),
	}
}
