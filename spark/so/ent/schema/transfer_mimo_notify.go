package schema

import (
	"context"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"go.uber.org/zap"
)

// mimoReceiverFanOutHook emits additional "transfer" events for secondary
// MIMO receivers whenever a Transfer transitions to SenderKeyTweaked.
//
// The standard NotifyMixin emits one event per Transfer mutation with the
// transfer's receiver_identity_pubkey in the payload. The DBEvents
// subscription system matches events by field/value, so only subscribers
// whose pubkey equals receiver_identity_pubkey (or sender_identity_pubkey)
// receive the event. Secondary MIMO receivers — tracked only in the
// transfer_receivers table — have no matching field and never get notified.
//
// This hook runs after the standard NotifyMixin. When the status field
// changes to SenderKeyTweaked, it queries TransferReceivers and emits one
// additional event per secondary receiver with their identity_pubkey as
// receiver_identity_pubkey. The event handler's processTransferNotification
// tolerates missing pubkey fields in these fan-out events (e.g.
// sender_identity_pubkey is omitted to avoid duplicate sender notifications).
func mimoReceiverFanOutHook() ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			value, err := next.Mutate(ctx, m)
			if err != nil {
				return value, err
			}

			// Only fan out when the status field was changed.
			if _, exists := m.Field("status"); !exists {
				return value, nil
			}

			transfer, ok := value.(*ent.Transfer)
			if !ok {
				return value, nil
			}

			// Only fan out for SenderKeyTweaked — the only status where
			// the event handler delivers receiver notifications.
			if transfer.Status != schematype.TransferStatusSenderKeyTweaked {
				return value, nil
			}

			logger := logging.GetLoggerFromContext(ctx)

			receivers, err := transfer.QueryTransferReceivers().All(ctx)
			if err != nil {
				logger.With(zap.Error(err)).Sugar().Warnf(
					"mimo fan-out: failed to query transfer receivers for %s", transfer.ID)
				return value, nil
			}
			if len(receivers) == 0 {
				return value, nil
			}

			notifier, err := ent.GetNotifierFromContext(ctx)
			if err != nil {
				logger.With(zap.Error(err)).Sugar().Warnf(
					"mimo fan-out: no notifier in context for transfer %s, skipping", transfer.ID)
				return value, nil
			}

			primaryPubkey := transfer.ReceiverIdentityPubkey.String()
			status := string(transfer.Status)

			for _, r := range receivers {
				receiverPubkey := r.IdentityPubkey.String()
				// Already covered by the standard NotifyMixin event.
				if receiverPubkey == primaryPubkey {
					continue
				}

				// Omit sender_identity_pubkey so this event only matches the
				// secondary receiver's subscription, not the sender's.
				if err := notifier.Notify(ctx, ent.Notification{
					Channel: "transfer",
					Payload: map[string]any{
						"id":                       transfer.ID.String(),
						"receiver_identity_pubkey": receiverPubkey,
						"status":                   status,
					},
				}); err != nil {
					logger.With(zap.Error(err)).Sugar().Warnf(
						"mimo fan-out: failed to emit event for receiver %s on transfer %s",
						receiverPubkey, transfer.ID)
				}
			}

			return value, nil
		})
	}
}
