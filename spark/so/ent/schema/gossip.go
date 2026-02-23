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
		field.Strings("participants").
			Immutable().
			Comment("List of operator identity public keys that should receive this gossip message.").
			Annotations(entexample.Default([]string{
				"0000000000000000000000000000000000000000000000000000000000000002",
				"0000000000000000000000000000000000000000000000000000000000000003",
			})),
		field.Bytes("message").
			NotEmpty().
			Immutable().
			Comment("Serialized GossipMessage proto payload to be delivered to participants.").
			Annotations(entexample.Default("0a0c48656c6c6f20576f726c642121")),
		field.Bytes("receipts").
			Nillable().
			Comment("Bitmap tracking which participants have received the message; bit positions correspond to order in the participants list.").
			Annotations(entexample.Default("03")),
		field.Enum("status").GoType(st.GossipStatus("")).Default(string(st.GossipStatusPending)).
			Comment("Delivery status of the gossip message; set to DELIVERED once all participants have received it."),
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
