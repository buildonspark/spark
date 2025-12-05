package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

type Gossip struct {
	ent.Schema
}

func (Gossip) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

func (Gossip) Fields() []ent.Field {
	return []ent.Field{
		// List of participants that should receive the message.
		field.Strings("participants").
			Immutable().
			Annotations(entexample.Default([]string{
				"0000000000000000000000000000000000000000000000000000000000000002",
				"0000000000000000000000000000000000000000000000000000000000000003",
			})),
		// The message payload. Serilalized GossipMessage in gossip.proto
		field.Bytes("message").
			NotEmpty().
			Immutable().
			Annotations(entexample.Default("0a0c48656c6c6f20576f726c642121")),
		// A bitmap of participants that have received the message, it maps with the order of participants.
		field.Bytes("receipts").
			Nillable().
			Annotations(entexample.Default("03")),
		// If all participants have received the message, the status is changed to DELIVERED.
		field.Enum("status").GoType(st.GossipStatus("")).Default(string(st.GossipStatusPending)),
	}
}

func (Gossip) Edges() []ent.Edge {
	return []ent.Edge{}
}

func (Gossip) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
	}
}
