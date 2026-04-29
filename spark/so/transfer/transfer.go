package transfer

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
)

// Maps the transfer status to a boolean indicating if the transfer is irrevokably sent.
func IsTransferSent(transfer *ent.Transfer) bool {
	switch transfer.Status {
	case st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
		st.TransferStatusCompleted:
		return true
	default:
		return false
	}
}

// MapTransferToReceiverStatus is the dual-write contract: given a
// transfers.status, returns the transfer_receivers.status that should
// hold for receivers in the pre-claim window. Used by:
//   - the dual-write mismatch monitor (so/task) to detect drift
//   - the SSP sync-transfer recovery path (so/handler) to create receiver
//     rows in the right initial state when reconstructing a transfer
//     from a remote SSP at a non-INITIATED status
//
// Returns SenderInitiated as a conservative default for unknown values.
func MapTransferToReceiverStatus(s st.TransferStatus) st.TransferReceiverStatus {
	switch s {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak:
		return st.TransferReceiverStatusSenderInitiated
	case st.TransferStatusSenderKeyTweaked:
		return st.TransferReceiverStatusReceiverClaimPending
	case st.TransferStatusReceiverKeyTweaked:
		return st.TransferReceiverStatusKeyTweaked
	case st.TransferStatusReceiverKeyTweakLocked:
		return st.TransferReceiverStatusKeyTweakLocked
	case st.TransferStatusReceiverKeyTweakApplied:
		return st.TransferReceiverStatusKeyTweakApplied
	case st.TransferStatusReceiverRefundSigned:
		return st.TransferReceiverStatusRefundSigned
	case st.TransferStatusCompleted:
		return st.TransferReceiverStatusCompleted
	case st.TransferStatusExpired, st.TransferStatusReturned:
		return st.TransferReceiverStatusCancelled
	default:
		return st.TransferReceiverStatusSenderInitiated
	}
}

// MarkReceiversClaimPending bulk-updates all transfer_receivers rows for the
// given transfer that are still in INITIATED to RECEIVER_CLAIM_PENDING. Called
// in the same transaction as the transfers.status flip to SENDER_KEY_TWEAKED;
// the dual-write contract is what lets the receiver-side pending query path
// filter on transfer_receivers.status alone (no JOIN-side t.status check
// needed).
//
// Idempotent — rows already in RECEIVER_CLAIM_PENDING (or any later state)
// are not touched, so this is safe to call from retry paths and from any
// flow that may have already been partially-completed.
func MarkReceiversClaimPending(ctx context.Context, db *ent.Client, transferID uuid.UUID) error {
	_, err := db.TransferReceiver.Update().
		Where(
			enttransferreceiver.TransferIDEQ(transferID),
			enttransferreceiver.StatusEQ(st.TransferReceiverStatusSenderInitiated),
		).
		SetStatus(st.TransferReceiverStatusReceiverClaimPending).
		Save(ctx)
	return err
}
