package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TransferStatus is the status of a transfer
type TransferStatus string

const (
	// TransferStatusInitiated is the status of a transfer that has been initiated.
	TransferStatusInitiated TransferStatus = "INITIATED"
	// TransferStatusKeyTweaked is the status of transfer where key has been tweaked.
	TransferStatusKeyTweaked TransferStatus = "KEY_TWEAKED"
	// TransferStatusRefundSigned is the status of transfer where refund transaction has been signed.
	TransferStatusRefundSigned TransferStatus = "REFUND_SIGNED"
	// TransferStatusCompleted is the status of transfer that has completed.
	TransferStatusCompleted TransferStatus = "COMPLETED"
	// TransferStatusExpired is the status of transfer that has expired and ownership has been returned to the transfer issuer.
	TransferStatusExpired TransferStatus = "EXPIRED"
)

// Values returns the values of the transfer status.
func (TransferStatus) Values() []string {
	return []string{
		string(TransferStatusInitiated),
		string(TransferStatusKeyTweaked),
		string(TransferStatusRefundSigned),
		string(TransferStatusCompleted),
		string(TransferStatusExpired),
	}
}

// Transfer is the schema for the transfer table.
type Transfer struct {
	ent.Schema
}

// Mixin is the mixin for the transfer table.
func (Transfer) Mixin() []ent.Mixin {
	return []ent.Mixin{
		BaseMixin{},
	}
}

// Fields are the fields for the tree nodes table.
func (Transfer) Fields() []ent.Field {
	return []ent.Field{
		field.Bytes("sender_identity_pubkey").NotEmpty().Immutable(),
		field.Bytes("receiver_identity_pubkey").NotEmpty().Immutable(),
		field.Uint64("total_value"),
		field.Enum("status").GoType(TransferStatus("")),
		field.Time("expiry_time").Immutable(),
	}
}

// Edges are the edges for the tree nodes table.
func (Transfer) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("transfer_leaves", TransferLeaf.Type).Ref("transfer"),
	}
}

// Indexes are the indexes for the tree nodes table.
func (Transfer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("sender_identity_pubkey"),
		index.Fields("receiver_identity_pubkey"),
		index.Fields("status"),
	}
}
