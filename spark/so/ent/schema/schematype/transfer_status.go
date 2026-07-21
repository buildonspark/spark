package schematype

// TransferStatus is the status of a transfer
type TransferStatus string

const (
	// TransferStatusSenderInitiated is the status of a transfer that has been initiated by sender.
	TransferStatusSenderInitiated TransferStatus = "SENDER_INITIATED"
	// TransferStatusSenderInitiatedCoordinator is the status of a transfer that has been initiated by sender directly to the coordinator.
	TransferStatusSenderInitiatedCoordinator TransferStatus = "SENDER_INITIATED_COORDINATOR"
	// TransferStatusSenderKeyTweakPending is the status of a transfer that has been initiated by sender but the key tweak is pending.
	TransferStatusSenderKeyTweakPending TransferStatus = "SENDER_KEY_TWEAK_PENDING"

	// This status is used to mark the transfer that is in the process of settling the sender key tweak and must not be cancelled.
	// [TransferStatusSenderInitiatedCoordinator, TransferStatusSenderKeyTweakPending] -> TransferStatusApplyingSenderKeyTweak -> TransferStatusSenderKeyTweaked
	TransferStatusApplyingSenderKeyTweak TransferStatus = "APPLYING_SENDER_KEY_TWEAK"

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
		string(TransferStatusApplyingSenderKeyTweak),
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

// IsTerminal reports whether a transfer in this status has finished its
// lifecycle. Drives the occupancy metrics: terminal statuses are never
// counted. Deliberately broader than idx_transfers_active_network_time's
// in-flight predicate, which omits APPLYING_SENDER_KEY_TWEAK.
func (s TransferStatus) IsTerminal() bool {
	switch s {
	case TransferStatusCompleted,
		TransferStatusExpired,
		TransferStatusReturned:
		return true
	case TransferStatusSenderInitiated,
		TransferStatusSenderInitiatedCoordinator,
		TransferStatusSenderKeyTweakPending,
		TransferStatusApplyingSenderKeyTweak,
		TransferStatusSenderKeyTweaked,
		TransferStatusReceiverKeyTweaked,
		TransferStatusReceiverKeyTweakLocked,
		TransferStatusReceiverKeyTweakApplied,
		TransferStatusReceiverRefundSigned:
		return false
	}
	// A status added without classification defaults to non-terminal so it
	// appears in occupancy counts rather than silently vanishing.
	return false
}

// NonTerminalTransferStatuses returns the statuses counted by the
// occupancy metrics.
func NonTerminalTransferStatuses() []TransferStatus {
	var out []TransferStatus
	for _, v := range (TransferStatus("")).Values() {
		if s := TransferStatus(v); !s.IsTerminal() {
			out = append(out, s)
		}
	}
	return out
}
