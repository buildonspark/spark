// Package mimo holds shared definitions and utilities for the MIMO
// (multi-input, multi-output) data model migration. The package is the
// single source of truth for status sets that are queried across multiple
// handlers, and a home for migration-phase code that bridges the legacy
// column model and the edge-table model.
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
// IS included in mimoStuckSenderStatuses (in ssp_request_handler.go). This
// inconsistency is preserved from the legacy queryTransfers behavior and
// is flagged for review in SP-2917 (mimo package consolidation) — the
// coordinator-side state is transient and historically wasn't surfaced to
// user-facing pending queries; whether this is intentional or oversight
// hasn't been decided.
func PendingSenderStatuses() []string {
	return []string{
		string(st.TransferStatusSenderKeyTweakPending),
		string(st.TransferStatusSenderInitiated),
	}
}
