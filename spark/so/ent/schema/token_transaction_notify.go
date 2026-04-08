package schema

import (
	"context"
	"encoding/hex"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
)

// tokenTransactionParticipantFanOutHook emits per-participant "tokentransaction"
// events whenever a TokenTransaction transitions to FINALIZED.
//
// When the status field changes to FINALIZED, it queries spent + created
// outputs to collect distinct owner_public_key values and emits one event per
// participant with their pubkey as owner_public_key. The event handler
// subscribes to owner_public_key=<identity> so only involved wallets receive
// the event.
//
// Guarded by KnobTokenTxEventsEnabled. When disabled (default), the hook is
// a complete no-op and no token transaction events are emitted.
func tokenTransactionParticipantFanOutHook() ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			value, err := next.Mutate(ctx, m)
			if err != nil {
				return value, err
			}

			if knobs.GetKnobsService(ctx).GetValue(knobs.KnobTokenTxEventsEnabled, 0) <= 0 {
				return value, nil
			}

			// Only fire when the status field was changed.
			if _, exists := m.Field("status"); !exists {
				return value, nil
			}

			tx, ok := value.(*ent.TokenTransaction)
			if !ok {
				return value, nil
			}

			if tx.Status != schematype.TokenTransactionStatusFinalized {
				return value, nil
			}

			logger := logging.GetLoggerFromContext(ctx)

			spentOutputs, err := tx.QuerySpentOutput().All(ctx)
			if err != nil {
				logger.With(zap.Error(err)).Sugar().Warnf(
					"token tx fan-out: failed to query spent outputs for %s", tx.ID)
				return value, nil
			}

			createdOutputs, err := tx.QueryCreatedOutput().All(ctx)
			if err != nil {
				logger.With(zap.Error(err)).Sugar().Warnf(
					"token tx fan-out: failed to query created outputs for %s", tx.ID)
				return value, nil
			}

			// Collect distinct participant pubkeys from both spent and created outputs.
			participants := make(map[string]struct{})
			for _, output := range spentOutputs {
				participants[hex.EncodeToString(output.OwnerPublicKey.Serialize())] = struct{}{}
			}
			for _, output := range createdOutputs {
				participants[hex.EncodeToString(output.OwnerPublicKey.Serialize())] = struct{}{}
			}

			if len(participants) == 0 {
				return value, nil
			}

			notifier, err := ent.GetNotifierFromContext(ctx)
			if err != nil {
				logger.With(zap.Error(err)).Sugar().Warnf(
					"token tx fan-out: no notifier in context for token transaction %s, skipping", tx.ID)
				return value, nil
			}

			status := string(tx.Status)

			for pubkeyHex := range participants {
				if err := notifier.Notify(ctx, ent.Notification{
					Channel: "tokentransaction",
					Payload: map[string]any{
						"id":               tx.ID.String(),
						"owner_public_key": pubkeyHex,
						"status":           status,
					},
				}); err != nil {
					logger.With(zap.Error(err)).Sugar().Warnf(
						"token tx fan-out: failed to emit event for participant %s on token transaction %s",
						pubkeyHex, tx.ID)
				}
			}

			return value, nil
		})
	}
}
