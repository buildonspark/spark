package mimo

import (
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// PendingReceiverStatuses are transfer_receivers.status values that mean
// the sender has completed key-tweak handoff and the receiver still has
// leaves to claim.
//
// INITIATED is deliberately excluded: it's the pre-tweak state, where the
// sender hasn't finished its handoff and the receiver cannot act.
func PendingReceiverStatuses() []string {
	return []string{
		string(st.TransferReceiverStatusReceiverClaimPending), // RECEIVER_CLAIM_PENDING
		string(st.TransferReceiverStatusKeyTweaked),           // RECEIVER_KEY_TWEAKED
		string(st.TransferReceiverStatusKeyTweakLocked),       // RECEIVER_KEY_TWEAK_LOCKED
		string(st.TransferReceiverStatusKeyTweakApplied),      // RECEIVER_KEY_TWEAK_APPLIED
		string(st.TransferReceiverStatusRefundSigned),         // RECEIVER_REFUND_SIGNED
	}
}

// PendingSenderStatuses are transfers.status values that mean the sender
// side hasn't completed its key-tweak handoff yet.
//
// Note: this set deliberately excludes SENDER_INITIATED_COORDINATOR, which
// IS included in StuckSenderStatuses. The pattern on the receiver side is
// pending = stuck + INITIATED (clean superset); the sender side breaks
// that pattern — pending is a strict subset of stuck, missing the
// coordinator-side state. SENDER_INITIATED_COORDINATOR is transitional
// and never set for more than a brief moment within a flow, so its
// absence from user-facing pending queries is effectively a no-op.
func PendingSenderStatuses() []string {
	return []string{
		string(st.TransferStatusSenderKeyTweakPending),
		string(st.TransferStatusSenderInitiated),
	}
}

// StuckSenderStatuses are the transfers.status values that mean the
// sender side hasn't completed its key-tweak handoff yet, surfaced to
// operators via GetStuckTransfers. Unlike PendingSenderStatuses this
// includes SENDER_INITIATED_COORDINATOR — see the note on
// PendingSenderStatuses for the historical reason the two sets diverge.
func StuckSenderStatuses() []string {
	return []string{
		string(st.TransferStatusSenderKeyTweakPending),
		string(st.TransferStatusSenderInitiated),
		string(st.TransferStatusSenderInitiatedCoordinator),
	}
}

// StuckReceiverStatuses are the transfer_receivers.status values that
// mean a receiver's claim is in flight but not yet settled.
//
// RECEIVER_CLAIM_PENDING is deliberately excluded: a receiver in that
// state has been handed off cleanly and is simply awaiting the user to
// claim — not stuck from the operator's perspective.
func StuckReceiverStatuses() []string {
	return []string{
		string(st.TransferReceiverStatusKeyTweaked),
		string(st.TransferReceiverStatusKeyTweakLocked),
		string(st.TransferReceiverStatusKeyTweakApplied),
		string(st.TransferReceiverStatusRefundSigned),
	}
}
