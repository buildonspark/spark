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
	_, connector, dbEvents := SetUpDBEventsTestContext(t)

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

	query := fmt.Sprintf("NOTIFY test, '%s'", payloadJSON)

	// Due to a race condition, the notification may be sent before the listener is set up
	// Send multiple notifications to cover the race condition
	numAttempts := 5
	received := false
	for i := 0; i < numAttempts; i++ {
		_, err = connector.Pool().Exec(t.Context(), query)
		require.NoError(t, err)

		select {
		case receivedPayload := <-channel:
			require.JSONEq(t, string(payloadJSON), receivedPayload.Payload)
			received = true
		case <-time.After(200 * time.Millisecond):
			t.Logf("Failed to receive message after 200ms, retrying...")
		}
	}

	require.True(t, received)
}

func TestDBEventsReconnect(t *testing.T) {
	_, connector, dbEvents := SetUpDBEventsTestContext(t)

	channel, _ := dbEvents.AddListeners([]Subscription{
		{
			EventName: "test",
			Field:     "id",
			Value:     "test_id",
		},
	})

	err := dbEvents.conn.Close(t.Context())
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return !dbEvents.conn.IsClosed()
	}, 5*time.Second, 100*time.Millisecond, "Connection should be reestablished")

	testPayload := map[string]any{
		"id": "test_id",
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
	_, connector, dbEvents := SetUpDBEventsTestContext(t)

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

	time.Sleep(100 * time.Millisecond)

	testPayload := map[string]any{
		"id": "test_id",
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
