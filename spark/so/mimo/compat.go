package mimo

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
)

// GetSingleTransferSender returns the sender identity pubkey for a transfer.
// When KnobReadMIMODataModelTransferSend is enabled, reads from TransferSenders edges
// (requires WithTransferSenders()); otherwise falls back to the deprecated column.
// If the knob is on but the edges are empty (un-backfilled transfer), falls back to the
// deprecated column and logs a warning. Errors if > 1 sender. For MIMO v1 multi-sender, see SP-2784.
func GetSingleTransferSender(ctx context.Context, t *ent.Transfer) (keys.Public, error) {
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelTransferSend, 0) > 0 {
		switch len(t.Edges.TransferSenders) {
		case 1:
			return t.Edges.TransferSenders[0].IdentityPubkey, nil
		case 0:
			logging.GetLoggerFromContext(ctx).Warn("MIMO sender edge missing, falling back to deprecated column",
				zap.String("transfer_id", t.ID.String()))
			return t.SenderIdentityPubkey, nil
		default:
			return keys.Public{}, fmt.Errorf("transfer %s has %d transfer senders, expected 1", t.ID, len(t.Edges.TransferSenders))
		}
	}
	return t.SenderIdentityPubkey, nil
}

// GetSingleTransferSenderReceiver returns the sender and receiver identity pubkeys for a transfer.
// When KnobReadMIMODataModelTransferSend is enabled, reads from TransferSenders/TransferReceivers
// edges (requires WithTransferSenders() and WithTransferReceivers()); otherwise falls back to
// the deprecated columns. If the knob is on but edges are empty (un-backfilled transfer),
// falls back to the deprecated columns and logs a warning.
// Errors if > 1 sender or > 1 receiver. For MIMO v1 multi-sender, see SP-2784.
func GetSingleTransferSenderReceiver(ctx context.Context, t *ent.Transfer) (sender, receiver keys.Public, err error) {
	senderPK, err := GetSingleTransferSender(ctx, t)
	if err != nil {
		return keys.Public{}, keys.Public{}, err
	}
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelTransferSend, 0) > 0 {
		switch len(t.Edges.TransferReceivers) {
		case 1:
			return senderPK, t.Edges.TransferReceivers[0].IdentityPubkey, nil
		case 0:
			logging.GetLoggerFromContext(ctx).Warn("MIMO receiver edge missing, falling back to deprecated column",
				zap.String("transfer_id", t.ID.String()))
			return senderPK, t.ReceiverIdentityPubkey, nil
		default:
			return keys.Public{}, keys.Public{}, fmt.Errorf("transfer %s has %d transfer receivers, expected 1", t.ID, len(t.Edges.TransferReceivers))
		}
	}
	return senderPK, t.ReceiverIdentityPubkey, nil
}
