package schematype

import (
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

// TransferStatus is the status of a transfer
type TransferStatus string

const (
	// TransferStatusSenderInitiated is the status of a transfer that has been initiated by sender.
	TransferStatusSenderInitiated TransferStatus = "SENDER_INITIATED"
	// TransferStatusSenderInitiatedCoordinator is the status of a transfer that has been initiated by sender directly to the coordinator.
	TransferStatusSenderInitiatedCoordinator TransferStatus = "SENDER_INITIATED_COORDINATOR"
	// TransferStatusSenderKeyTweakPending is the status of a transfer that has been initiated by sender but the key tweak is pending.
	TransferStatusSenderKeyTweakPending TransferStatus = "SENDER_KEY_TWEAK_PENDING"
	// TransferStatusSenderKeyTweaked is the status of a transfer that sender has tweaked the key.
	TransferStatusSenderKeyTweaked TransferStatus = "SENDER_KEY_TWEAKED"
	// TransferStatusReceiverKeyTweaked is the status of transfer where key has been tweaked.
	TransferStatusReceiverKeyTweaked TransferStatus = "RECEIVER_KEY_TWEAKED"
	// TransferStatusReceiverKeyTweakLocked is the status of transfer where key has been tweaked and locked.
	TransferStatusReceiverKeyTweakLocked TransferStatus = "RECEIVER_KEY_TWEAK_LOCKED"
	// TransferStatusReceiverKeyTweakApplied is the status of transfer where key has been tweaked and applied.
	TransferStatusReceiverKeyTweakApplied TransferStatus = "RECEIVER_KEY_TWEAK_APPLIED"
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
		string(TransferStatusSenderInitiatedCoordinator),
		string(TransferStatusSenderKeyTweakPending),
		string(TransferStatusSenderKeyTweaked),
		string(TransferStatusReceiverKeyTweaked),
		string(TransferStatusReceiverKeyTweakLocked),
		string(TransferStatusReceiverRefundSigned),
		string(TransferStatusCompleted),
		string(TransferStatusExpired),
		string(TransferStatusReturned),
		string(TransferStatusReceiverKeyTweakApplied),
	}
}

func SchemaTransferStatusFromProtoTransferStatus(protoTransferStatus pb.TransferStatus) (TransferStatus, error) {
	switch protoTransferStatus {
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED:
		return TransferStatusSenderInitiated, nil
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR:
		return TransferStatusSenderInitiatedCoordinator, nil
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING:
		return TransferStatusSenderKeyTweakPending, nil
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED:
		return TransferStatusSenderKeyTweaked, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED:
		return TransferStatusReceiverKeyTweaked, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED:
		return TransferStatusReceiverKeyTweakLocked, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED:
		return TransferStatusReceiverKeyTweakApplied, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED:
		return TransferStatusReceiverRefundSigned, nil
	case pb.TransferStatus_TRANSFER_STATUS_COMPLETED:
		return TransferStatusCompleted, nil
	case pb.TransferStatus_TRANSFER_STATUS_EXPIRED:
		return TransferStatusExpired, nil
	case pb.TransferStatus_TRANSFER_STATUS_RETURNED:
		return TransferStatusReturned, nil
	default:
		return "", fmt.Errorf("unknown transfer status: %v", protoTransferStatus)
	}
}
