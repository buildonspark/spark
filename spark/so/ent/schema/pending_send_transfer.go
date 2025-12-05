package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entexample"
)

// PendingSendTransfer holds the state of a send transfer that is pending.
type PendingSendTransfer struct {
	ent.Schema
}

// Mixin is the mixin for the PendingSendTransfer table.
func (PendingSendTransfer) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the PendingSendTransfer table.
func (PendingSendTransfer) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("transfer_id", uuid.UUID{}).
			Unique().
			Immutable().
			Annotations(entexample.Default("019a02dd-8468-7bfe-9a73-d9b03681710e")),
		field.Enum("status").
			GoType(st.PendingSendTransferStatus("")).
			Default(string(st.PendingSendTransferStatusPending)).
			Annotations(entexample.Default(st.PendingSendTransferStatusPending)),
	}
}

// Indexes are the indexes for the PendingSendTransfer table.
func (PendingSendTransfer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("transfer_id").Unique(),
		index.Fields("status"),
	}
}

// Edges are the edges for the PendingSendTransfer table.
func (PendingSendTransfer) Edges() []ent.Edge {
	return []ent.Edge{}
}
