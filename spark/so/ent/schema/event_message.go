package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/lightsparkdev/spark/so/entexample"
)

// EventMessage stores notifications emitted by ent mutations so they can be
// consumed by pollers without using Postgres NOTIFY.
type EventMessage struct {
	ent.Schema
}

func (EventMessage) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (EventMessage) Fields() []ent.Field {
	return []ent.Field{
		field.Text("channel").
			NotEmpty().
			Comment("The channel on which the event was emitted.").
			Annotations(entexample.Default("transfer")),
		field.Text("payload").
			NotEmpty().
			Comment("The JSON payload describing the even that occurred (i.e. the ent that was updated & relevant details).").
			Annotations(entexample.Default("{\"id\":\"019af0a2-8f2d-753e-9dd3-d96d5a56f254\",\"receiver_identity_pubkey\":\"02fa14545dc12d8b64c05bf5f3fba3ba5a9311af11dffd702465142c83e45fd2c4\",\"status\":\"COMPLETED\"}")),
	}
}

func (EventMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel", "create_time", "id").
			StorageKey("event_messages_channel_create_time_id"),
	}
}
