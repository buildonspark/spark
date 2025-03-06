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
	// TransferStatusSenderInitiated is the status of a transfer that has been initiated by sender.
	TransferStatusSenderInitiated TransferStatus = "SENDER_INITIATED"
	// TransferStatusSenderKeyTweakPending is the status of a transfer that has been initiated by sender but the key tweak is pending.
	TransferStatusSenderKeyTweakPending TransferStatus = "SENDER_KEY_TWEAK_PENDING"
	// TransferStatusSenderKeyTweaked is the status of a transfer that sender has tweaked the key.
	TransferStatusSenderKeyTweaked TransferStatus = "SENDER_KEY_TWEAKED"
	// TransferStatusReceiverKeyTweaked is the status of transfer where key has been tweaked.
	TransferStatusReceiverKeyTweaked TransferStatus = "RECEIVER_KEY_TWEAKED"
	// TransferStatusReceiverRefundSigned is the status of transfer where refund transaction has been signed.
	TransferStatusReceiverRefundSigned TransferStatus = "RECEIVER_REFUND_SIGNED"
	// TransferStatusCompleted is the status of transfer that has completed.
	TransferStatusCompleted TransferStatus = "COMPLETED"
	// TransferStatusExpired is the status of transfer that has expired and ownership has been returned to the transfer issuer.
	TransferStatusExpired TransferStatus = "EXPIRED"
	// TransferStatusReturned is the status of transfer that has been returned to the sender.
	TransferStatusReturned TransferStatus = "RETURNED"
)

// Values returns the values of the transfer status.
func (TransferStatus) Values() []string {
	return []string{
		string(TransferStatusSenderInitiated),
		string(TransferStatusSenderKeyTweakPending),
		string(TransferStatusSenderKeyTweaked),
		string(TransferStatusReceiverKeyTweaked),
		string(TransferStatusReceiverRefundSigned),
		string(TransferStatusCompleted),
		string(TransferStatusExpired),
		string(TransferStatusReturned),
	}
}

// TransferType is the type of transfer
type TransferType string

const (
	// TransferTypePreimageSwap is the type of transfer that is a preimage swap
	TransferTypePreimageSwap TransferType = "PREIMAGE_SWAP"
	// TransferTypeCooperativeExit is the type of transfer that is a cooperative exit
	TransferTypeCooperativeExit TransferType = "COOPERATIVE_EXIT"
	// TransferTypeTransfer is the type of transfer that is a normal transfer
	TransferTypeTransfer TransferType = "TRANSFER"
)

// Values returns the values of the transfer type.
func (TransferType) Values() []string {
	return []string{
		string(TransferTypePreimageSwap),
		string(TransferTypeCooperativeExit),
		string(TransferTypeTransfer),
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
		field.Enum("type").GoType(TransferType("")),
		field.Time("expiry_time").Immutable(),
		field.Time("completion_time").Optional().Nillable(),
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
