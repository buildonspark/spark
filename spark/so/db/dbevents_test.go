package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRegisteringListeners(t *testing.T) {
	_, _, dbEvents := SetupDBEventsTestContext(t)

	_, cleanupListener := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id",
		},
	})
	defer cleanupListener()

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 1)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test-id",
	}], 1)

	_, cleanupListener2 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id",
		},
	})
	defer cleanupListener2()

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 1)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test-id",
	}], 2)

	_, cleanupListener3 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id-1",
		},
	})
	defer cleanupListener3()

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 2)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test-id",
	}], 2)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test-id-1",
	}], 1)

	_, cleanupListener4 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test-1",
			Field:     "id",
			Value:     "test-id-2",
		},
		{
			EventName: "test-2",
			Field:     "id",
			Value:     "test-id-3",
		},
	})
	defer cleanupListener4()

	require.Len(t, dbEvents.listeners, 3)
	require.Len(t, dbEvents.listeners["test-1"], 1)
	require.Len(t, dbEvents.listeners["test-1"][listenerKey{
		Field: "id",
		Value: "test-id-2",
	}], 1)
	require.Len(t, dbEvents.listeners["test-2"], 1)
	require.Len(t, dbEvents.listeners["test-2"][listenerKey{
		Field: "id",
		Value: "test-id-3",
	}], 1)
}

func TestCleaningUpListeners(t *testing.T) {
	_, _, dbEvents := SetupDBEventsTestContext(t)

	_, cleanupListener := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id",
		},
	})

	require.Len(t, dbEvents.listeners, 1)
	require.Len(t, dbEvents.listeners["test"], 1)
	require.Len(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test-id",
	}], 1)

	cleanupListener()

	require.Empty(t, dbEvents.listeners)
	require.Empty(t, dbEvents.listeners["test"])
	require.Empty(t, dbEvents.listeners["test"][listenerKey{
		Field: "id",
		Value: "test-id",
	}])
}

func TestDBEvents(t *testing.T) {
	_, connector, dbEvents := SetupDBEventsTestContext(t)

	channel, _ := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id",
		},
	})

	time.Sleep(100 * time.Millisecond)

	testPayload := map[string]any{
		"id": "test-id",
	}

	payloadJSON, err := json.Marshal(testPayload)
	require.NoError(t, err)

	query := fmt.Sprintf("NOTIFY test, '%s'", payloadJSON)
	_, err = connector.Pool().Exec(t.Context(), query)
	require.NoError(t, err)

	select {
	case receivedPayload := <-channel:
		require.JSONEq(t, string(payloadJSON), receivedPayload.Payload)
	case <-time.After(6 * time.Second):
		t.Fatal("Timeout waiting for notification")
	}
}

func TestDBEventsReconnect(t *testing.T) {
	_, connector, dbEvents := SetupDBEventsTestContext(t)

	channel, _ := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id",
		},
	})

	err := dbEvents.conn.Close(t.Context())
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return !dbEvents.conn.IsClosed()
	}, 5*time.Second, 100*time.Millisecond, "Connection should be reestablished")

	testPayload := map[string]any{
		"id": "test-id",
	}

	payloadJSON, err := json.Marshal(testPayload)
	require.NoError(t, err)

	query := fmt.Sprintf("NOTIFY test, '%s'", payloadJSON)
	_, err = connector.Pool().Exec(t.Context(), query)
	require.NoError(t, err)

	select {
	case receivedPayload := <-channel:
		require.JSONEq(t, string(payloadJSON), receivedPayload.Payload)
	case <-time.After(6 * time.Second):
		t.Fatal("Timeout waiting for notification")
	}
}

func TestMultipleListenersReceiveNotification(t *testing.T) {
	_, connector, dbEvents := SetupDBEventsTestContext(t)

	channel1, cleanupListener := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id",
		},
	})
	defer cleanupListener()

	channel2, cleanupListener2 := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test-id",
		},
	})
	defer cleanupListener2()

	time.Sleep(100 * time.Millisecond)

	testPayload := map[string]any{
		"id": "test-id",
	}

	payloadJSON, err := json.Marshal(testPayload)
	require.NoError(t, err)

	query := fmt.Sprintf("NOTIFY test, '%s'", payloadJSON)
	_, err = connector.Pool().Exec(t.Context(), query)
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
