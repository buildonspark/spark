package transfer

import (
	"context"
	"fmt"

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

// MapTransferToReceiverStatus maps a transfers.status to the
// transfer_receivers.status a receiver should hold at that point. Valid only
// for single-receiver transfers: under receiver-authoritative status a
// multi-receiver parent stays at SENDER_KEY_TWEAKED while its receivers advance
// independently, so no receiver status is derivable from the parent. The sole
// caller — the SSP sync-transfer recovery path (so/handler), which recreates a
// receiver row when reconstructing a transfer from a remote SSP at a
// non-INITIATED status — is single-receiver-only, so the mapping is valid there.
//
// Returns SenderInitiated as a conservative default for unknown values.
func MapTransferToReceiverStatus(s st.TransferStatus) st.TransferReceiverStatus {
	switch s {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak:
		return st.TransferReceiverStatusInitiated
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
		return st.TransferReceiverStatusInitiated
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
			enttransferreceiver.StatusEQ(st.TransferReceiverStatusInitiated),
		).
		SetStatus(st.TransferReceiverStatusReceiverClaimPending).
		Save(ctx)
	return err
}

// ResetReceiversToTransferStatus forces every transfer_receivers row for the
// transfer to the status the given non-terminal transfer status maps to,
// whatever it currently holds. Unlike MarkReceiversClaimPending this does not
// filter on the current status: a receiver left CANCELLED by an earlier
// terminal transition would otherwise keep the receiver-side pending query —
// which filters on transfer_receivers.status alone — from ever returning the
// transfer again.
//
// Terminal statuses are rejected because their receiver rows also carry
// completion_time rules; route those through the terminal sync instead.
func ResetReceiversToTransferStatus(ctx context.Context, db *ent.Client, transferID uuid.UUID, transferStatus st.TransferStatus) error {
	if transferStatus.IsTerminal() {
		return fmt.Errorf("ResetReceiversToTransferStatus called with terminal transfer status %s", transferStatus)
	}
	_, err := db.TransferReceiver.Update().
		Where(enttransferreceiver.TransferIDEQ(transferID)).
		SetStatus(MapTransferToReceiverStatus(transferStatus)).
		ClearCompletionTime().
		Save(ctx)
	return err
}
