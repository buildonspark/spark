package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	stop := StartPostgresServer()
	defer stop()

	m.Run()
}

func TestRegisteringListeners(t *testing.T) {
	t.Parallel()
	_, _, dbEvents := SetUpDBEventsTestContext(t)

	_, cleanupListener := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})
	defer cleanupListener()

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 1)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test_id",
	}], 1)

	_, cleanupListener2 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})
	defer cleanupListener2()

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 1)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test_id",
	}], 2)

	_, cleanupListener3 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id_1",
		},
	})
	defer cleanupListener3()

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 2)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test_id",
	}], 2)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test_id_1",
	}], 1)

	_, cleanupListener4 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test_1",
			Field:     "id",
			Value:     "test_id_2",
		},
		{
			EventName: "test_2",
			Field:     "id",
			Value:     "test_id_3",
		},
	})
	defer cleanupListener4()

	require.Len(t, dbEvents.listeners, 3)
	require.Len(t, dbEvents.listeners["test_1"], 1)
	require.Len(t, dbEvents.listeners["test_1"][listenerKey{
		Field: "id",
		Value: "test_id_2",
	}], 1)
	require.Len(t, dbEvents.listeners["test_2"], 1)
	require.Len(t, dbEvents.listeners["test_2"][listenerKey{
		Field: "id",
		Value: "test_id_3",
	}], 1)
}

func TestCleaningUpListeners(t *testing.T) {
	t.Parallel()
	_, _, dbEvents := SetUpDBEventsTestContext(t)

	_, cleanupListener := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 1)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test_id",
	}], 1)

	cleanupListener()

	require.Empty(t, dbEvents.listeners)
	require.Empty(t, dbEvents.listeners["test"])
	require.Empty(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test_id",
	}])
}

func TestDBEvents(t *testing.T) {
	t.Parallel()
	ctx, _, dbEvents := SetUpDBEventsTestContext(t)

	channel, _ := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})

	testPayload := map[string]any{
		"id": "test_id",
	}

	payloadJSON, err := json.Marshal(testPayload)
	require.NoError(t, err)

	for range 5 {
		_, err = ctx.Client.EventMessage.Create().
			SetChannel("test").
			SetPayload(string(payloadJSON)).
			Save(t.Context())
		require.NoError(t, err)

		select {
		case receivedPayload := <-channel:
			require.JSONEq(t, string(payloadJSON), receivedPayload.Payload)
			return
		case <-time.After(200 * time.Millisecond):
			t.Logf("Failed to receive message after 200ms, retrying...")
		}
	}

	t.Fatal("failed to receive notification")
}

func TestMultipleListenersReceiveNotification(t *testing.T) {
	ctx, _, dbEvents := SetUpDBEventsTestContext(t)

	channel1, cleanupListener := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})
	defer cleanupListener()

	channel2, cleanupListener2 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})
	defer cleanupListener2()

	testPayload := map[string]any{
		"id": "test_id",
	}

	payloadJSON, err := json.Marshal(testPayload)
	require.NoError(t, err)

	_, err = ctx.Client.EventMessage.Create().
		SetChannel("test").
		SetPayload(string(payloadJSON)).
		Save(t.Context())
	require.NoError(t, err)

	var received1, received2 bool
	timeout := time.After(6 * time.Second)

	for !received1 || !received2 {
		select {
		case receivedPayload := <-channel1:
			require.JSONEq(t, string(payloadJSON), receivedPayload.Payload)
			received1 = true
		case receivedPayload := <-channel2:
			require.JSONEq(t, string(payloadJSON), receivedPayload.Payload)
			received2 = true
		case <-timeout:
			t.Fatalf("Timeout waiting for notification. received1: %v, received2: %v", received1, received2)
		}
	}
}

func TestListenerCleanupRemovesEntries(t *testing.T) {
	_, _, dbEvents := SetUpDBEventsTestContext(t)

	subscription := Subscription{
		EventName: "test",
		Field:     "id",
		Value:     "test_id",
	}

	_, cleanup := dbEvents.AddListeners([]Subscription{subscription})

	dbEvents.mu.RLock()
	require.NotEmpty(t, dbEvents.listeners)
	dbEvents.mu.RUnlock()

	cleanup()

	dbEvents.mu.RLock()
	defer dbEvents.mu.RUnlock()
	_, exists := dbEvents.listeners[subscription.EventName]
	require.False(t, exists, "listener map should be cleaned up")
}

func TestDBEventsDoesNotRedeliverMessages(t *testing.T) {
	ctx, _, dbEvents := SetUpDBEventsTestContext(t)

	channel, cleanup := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})
	defer cleanup()

	payload := `{"id":"test_id"}`

	_, err := ctx.Client.EventMessage.Create().
		SetChannel("test").
		SetPayload(payload).
		Save(t.Context())
	require.NoError(t, err)

	select {
	case receivedPayload := <-channel:
		require.JSONEq(t, payload, receivedPayload.Payload)
	case <-time.After(time.Second):
		t.Fatal("expected to receive initial notification")
	}

	select {
	case <-channel:
		t.Fatal("unexpected duplicate message")
	case <-time.After(300 * time.Millisecond):
		// no-op; ensured we didn't redeliver
	}
}

func TestDBEventsProcessesBatchesAcrossCursor(t *testing.T) {
	ctx, _, dbEvents := SetUpDBEventsTestContext(t)
	dbEvents.batchSize = 3

	const (
		channelName       = "test_batch"
		subscriptionField = "status"
		subscriptionValue = "batch"
	)

	channel, cleanup := dbEvents.AddListeners([]Subscription{
		{
			EventName: channelName,
			Field:     subscriptionField,
			Value:     subscriptionValue,
		},
	})
	defer cleanup()

	createEvents := func(start, count int) []string {
		ids := make([]string, count)
		for i := range count {
			id := fmt.Sprintf("event-%d", start+i)
			payload := map[string]any{
				"id":              id,
				subscriptionField: subscriptionValue,
			}
			payloadJSON, err := json.Marshal(payload)
			require.NoError(t, err)

			_, err = ctx.Client.EventMessage.Create().
				SetChannel(channelName).
				SetPayload(string(payloadJSON)).
				Save(t.Context())
			require.NoError(t, err)
			ids[i] = id
		}
		return ids
	}

	receiveEventIDs := func(expected int) []string {
		ids := make([]string, 0, expected)
		timeout := time.After(5 * time.Second)
		for len(ids) < expected {
			select {
			case evt := <-channel:
				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(evt.Payload), &payload))
				id, ok := payload["id"].(string)
				require.True(t, ok)
				ids = append(ids, id)
			case <-timeout:
				t.Fatalf("Timeout waiting for events. received=%d expected=%d", len(ids), expected)
			}
		}
		return ids
	}

	firstBatch := createEvents(0, 12)
	require.ElementsMatch(t, firstBatch, receiveEventIDs(len(firstBatch)))

	secondBatch := createEvents(12, 5)
	require.ElementsMatch(t, secondBatch, receiveEventIDs(len(secondBatch)))

	select {
	case evt := <-channel:
		t.Fatalf("received unexpected extra event: %v", evt)
	case <-time.After(200 * time.Millisecond):
	}
}
