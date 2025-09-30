package schema

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"entgo.io/ent/schema/mixin"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
)

var globalNotifyCounter metric.Int64Counter

func init() {
	meter := otel.GetMeterProvider().Meter("spark.db.ent")
	notifyCounter, err := meter.Int64Counter(
		"spark_ent_notify_per_channel",
		metric.WithDescription("Count of Postgres NOTIFY per channel"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if notifyCounter == nil {
			notifyCounter = noop.Int64Counter{}
		}
	}

	globalNotifyCounter = notifyCounter
}

/*
The payload will always include the ID field.
Use AdditionalFields if other fields need to be included in the payload.
(e.g. a 'status' field so the listener can filter for certain statuses before querying the ent)
*/
type NotifyMixin struct {
	mixin.Schema
	AdditionalFields []string
}

func (n NotifyMixin) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				value, err := next.Mutate(ctx, m)
				if err != nil {
					return value, err
				}

				logger := logging.GetLoggerFromContext(ctx)

				if err := n.sendNotification(ctx, m, value); err != nil {
					logger.Error("Failed to send notification", zap.Error(err))
				}

				return value, nil
			})
		},
	}
}

func (n NotifyMixin) sendNotification(ctx context.Context, m ent.Mutation, v ent.Value) error {
	payload := n.buildPayload(v)

	notifier, err := ent.GetNotifierFromContext(ctx)
	if err != nil {
		return fmt.Errorf("no notifier found in context: %w", err)
	}

	notification := ent.Notification{
		Channel: m.Type(),
		Payload: payload,
	}

	globalNotifyCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("channel", notification.Channel)))

	return notifier.Notify(ctx, notification)
}

func (n NotifyMixin) buildPayload(v ent.Value) map[string]any {
	payload := make(map[string]any)

	raw, _ := json.Marshal(v)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)

	if id, ok := fields["id"]; ok {
		payload["id"] = id
	}

	for _, f := range n.AdditionalFields {
		if val, ok := fields[f]; ok {
			if decoded, err := base64.StdEncoding.DecodeString(val.(string)); err == nil {
				payload[f] = hex.EncodeToString(decoded)
			} else if bytes, ok := val.([]byte); ok {
				payload[f] = hex.EncodeToString(bytes)
			} else {
				payload[f] = val
			}
		}
	}

	return payload
}
