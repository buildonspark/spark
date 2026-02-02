package schema

import (
	"encoding/json"

	"entgo.io/ent"
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
		index.Fields("idempotency_key", "method_name").
			Unique().
			StorageKey("idempotency_keys_idempotency_key_method_name"),
		index.Fields("create_time").
			StorageKey("idempotency_keys_create_time"),
	}
}
