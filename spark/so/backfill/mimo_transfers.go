package backfill

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"go.uber.org/zap"
)

// BackfillMimoResult holds the results of both backfill operations.
type BackfillMimoResult struct {
	TransfersCreated        int
	ReceiverStatusesUpdated int
}

// BackfillMimoTransfers runs two backfill operations:
//  1. Creates TransferSender/TransferReceiver/TransferLeaf associations for
//     historical Transfers that predate MIMO writes.
//  2. Syncs stale TransferReceiver statuses for receivers created before
//     dual-write status updates were enabled.
func BackfillMimoTransfers(ctx context.Context, config *so.Config, batchSize int) (BackfillMimoResult, error) {
	created, err := backfillCreateMimoRecords(ctx, batchSize)
	if err != nil {
		return BackfillMimoResult{}, fmt.Errorf("backfill create records: %w", err)
	}

	updated, err := backfillSyncReceiverStatuses(ctx, batchSize)
	if err != nil {
		return BackfillMimoResult{}, fmt.Errorf("backfill sync receiver statuses: %w", err)
	}

	return BackfillMimoResult{
		TransfersCreated:        created,
		ReceiverStatusesUpdated: updated,
	}, nil
}

// backfillCreateMimoRecords finds Transfers without TransferSender records and
// creates the corresponding TransferSender, TransferReceiver, and TransferLeaf
// associations.
func backfillCreateMimoRecords(ctx context.Context, batchSize int) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get db from context: %w", err)
	}

	transfers, err := db.Transfer.Query().
		Where(
			enttransfer.Not(enttransfer.HasTransferSenders()),
			enttransfer.NetworkNEQ(btcnetwork.Unspecified),
		).
		Order(enttransfer.ByCreateTime(sql.OrderAsc())).
		Limit(batchSize).
		ForUpdate(sql.WithLockAction(sql.SkipLocked)).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to query transfers without senders: %w", err)
	}

	if len(transfers) == 0 {
		return 0, nil
	}

	processed := 0
	for _, t := range transfers {
		sender, err := db.TransferSender.Create().
			SetTransferID(t.ID).
			SetIdentityPubkey(t.SenderIdentityPubkey).
			Save(ctx)
		if err != nil {
			logger.Warn(fmt.Sprintf("backfill_mimo_transfers: failed to create sender for transfer %s, skipping", t.ID), zap.Error(err))
			continue
		}

		receiverCreate := db.TransferReceiver.Create().
			SetTransferID(t.ID).
			SetIdentityPubkey(t.ReceiverIdentityPubkey).
			SetStatus(MapTransferToReceiverStatus(t.Status))
		if t.CompletionTime != nil {
			receiverCreate = receiverCreate.SetNillableCompletionTime(t.CompletionTime)
		}
		receiver, err := receiverCreate.Save(ctx)
		if err != nil {
			logger.Warn(fmt.Sprintf("backfill_mimo_transfers: failed to create receiver for transfer %s, skipping", t.ID), zap.Error(err))
			_ = db.TransferSender.DeleteOne(sender).Exec(ctx)
			continue
		}

		err = db.TransferLeaf.Update().
			Where(
				transferleaf.HasTransferWith(enttransfer.IDEQ(t.ID)),
				transferleaf.TransferSenderIDIsNil(),
			).
			SetTransferSenderID(sender.ID).
			SetTransferReceiverID(receiver.ID).
			Exec(ctx)
		if err != nil {
			logger.Warn(fmt.Sprintf("backfill_mimo_transfers: failed to update leaves for transfer %s, skipping", t.ID), zap.Error(err))
			_ = db.TransferReceiver.DeleteOne(receiver).Exec(ctx)
			_ = db.TransferSender.DeleteOne(sender).Exec(ctx)
			continue
		}

		processed++
	}

	return processed, nil
}

// backfillSyncReceiverStatuses finds TransferReceiver records whose status is out
// of sync with their Transfer and updates them. This covers the gap between when
// TransferReceiver records started being created and when dual-write status updates
// were enabled.
//
// To avoid fetching the same in-progress records repeatedly, we only target
// receivers whose Transfer has reached a terminal state (Completed/Expired/Returned)
// while the receiver itself has not yet been updated to the corresponding terminal
// status (Completed/Cancelled).
func backfillSyncReceiverStatuses(ctx context.Context, batchSize int) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get db from context: %w", err)
	}

	receivers, err := db.TransferReceiver.Query().
		Where(
			transferreceiver.StatusNotIn(
				st.TransferReceiverStatusCompleted,
				st.TransferReceiverStatusCancelled,
			),
			transferreceiver.HasTransferWith(
				enttransfer.StatusIn(
					st.TransferStatusCompleted,
					st.TransferStatusExpired,
					st.TransferStatusReturned,
				),
			),
		).
		WithTransfer().
		Limit(batchSize).
		ForUpdate(sql.WithLockAction(sql.SkipLocked)).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to query receivers with stale terminal status: %w", err)
	}

	if len(receivers) == 0 {
		return 0, nil
	}

	updated := 0
	for _, r := range receivers {
		transfer := r.Edges.Transfer
		if transfer == nil {
			continue
		}

		expectedStatus := MapTransferToReceiverStatus(transfer.Status)

		updateOp := r.Update().SetStatus(expectedStatus)
		if expectedStatus == st.TransferReceiverStatusCompleted && transfer.CompletionTime != nil {
			updateOp = updateOp.SetNillableCompletionTime(transfer.CompletionTime)
		}

		_, err = updateOp.Save(ctx)
		if err != nil {
			logger.Warn(fmt.Sprintf("backfill_mimo_receiver_statuses: failed to update receiver %s for transfer %s, skipping", r.ID, transfer.ID), zap.Error(err))
			continue
		}

		logger.Info(fmt.Sprintf("backfill_mimo_receiver_statuses: updated receiver %s status %s -> %s for transfer %s", r.ID, r.Status, expectedStatus, transfer.ID))
		updated++
	}

	return updated, nil
}

// MapTransferToReceiverStatus maps a Transfer status to the corresponding TransferReceiver status.
func MapTransferToReceiverStatus(s st.TransferStatus) st.TransferReceiverStatus {
	switch s {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweaked:
		return st.TransferReceiverStatusSenderInitiated
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
