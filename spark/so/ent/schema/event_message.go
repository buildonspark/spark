package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
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
		field.Text("channel").NotEmpty(),
		field.Text("payload").NotEmpty(),
	}
}

func (EventMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel", "create_time", "id").
			StorageKey("event_messages_channel_create_time_id"),
	}
}
