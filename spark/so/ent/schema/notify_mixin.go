package schema

import (
	"context"
	"encoding/json"
	"fmt"

	"entgo.io/ent/schema/mixin"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
)

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
					logger.Error("Failed to send notification", "error", err)
				}

				return value, nil
			})
		},
	}
}

func (n NotifyMixin) sendNotification(ctx context.Context, m ent.Mutation, v ent.Value) error {
	payload := n.buildPayload(v)

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	client := m.(interface {
		Client() *ent.Client
	}).Client()

	if client == nil {
		return fmt.Errorf("client is nil, cannot send notification")
	}

	channel := m.Type()
	query := fmt.Sprintf("NOTIFY %s, '%s'", channel, payloadJSON)

	// nolint:forbidigo
	_, err = client.ExecContext(ctx, query)
	return err
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
			payload[f] = val
		}
	}

	return payload
}
