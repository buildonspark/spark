package events

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/stretchr/testify/require"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type MockStream struct {
	ctx      context.Context
	messages []*pb.SubscribeToEventsResponse
	mu       sync.Mutex
	sendErr  error
}

func NewMockStream(t *testing.T) *MockStream {
	return &MockStream{
		ctx:      t.Context(),
		messages: make([]*pb.SubscribeToEventsResponse, 0),
	}
}

func (m *MockStream) Send(msg *pb.SubscribeToEventsResponse) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

func (m *MockStream) RecvMsg(_ any) error {
	return nil
}

func (m *MockStream) Context() context.Context {
	return m.ctx
}

func (m *MockStream) SendHeader(_ metadata.MD) error {
	return nil
}

func (m *MockStream) SendMsg(_ any) error {
	return nil
}

func (m *MockStream) SetHeader(_ metadata.MD) error {
	return nil
}

func (m *MockStream) SetTrailer(_ metadata.MD) {}

func TestEventRouterConcurrency(t *testing.T) {
	ctx, _, dbEvents := db.SetupDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := slog.Default().With("component", "events_router")
	router := NewEventRouter(dbClient, dbEvents, logger)
	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	const numGoroutines = 100
	var wg sync.WaitGroup

	makeStream := func(i int) *MockStream {
		switch i % 3 {
		case 0:
			// Normal stream
			ctx, cancel := context.WithCancel(t.Context())
			stream := &MockStream{ctx: ctx, messages: make([]*pb.SubscribeToEventsResponse, 0)}

			go func() {
				for {
					stream.mu.Lock()
					if len(stream.messages) > 0 {
						stream.mu.Unlock()
						break
					}
					stream.mu.Unlock()
				}
				cancel()
			}()
			stream.messages = make([]*pb.SubscribeToEventsResponse, 0)
			return stream
		case 1:
			// Stream that errors on send
			return &MockStream{
				ctx:     t.Context(),
				sendErr: status.Error(codes.Unavailable, "stream closed"),
			}
		default:
			// Stream with cancellable context
			ctx, cancel := context.WithCancel(t.Context())
			stream := &MockStream{ctx: ctx}
			// Cancel after a short delay
			go func() {
				time.Sleep(time.Millisecond)
				cancel()
			}()
			return stream
		}
	}

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			stream := makeStream(idx)

			err := router.SubscribeToEvents(identityKey, stream)
			if err != nil {
				t.Errorf("Failed to register stream: %v", err)
			}
		}(i)
	}

	wg.Wait()
}

func TestMultipleListenersReceiveNotification(t *testing.T) {
	ctx, _, dbEvents := db.SetupDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := slog.Default().With("component", "events_router")
	router := NewEventRouter(dbClient, dbEvents, logger)
	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	ctx1, cancel1 := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel1()
	stream1 := &MockStream{ctx: ctx1, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel2()
	stream2 := &MockStream{ctx: ctx2, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	var wg sync.WaitGroup
	var stream1Err, stream2Err error
	wg.Add(2)

	go func() {
		defer wg.Done()
		stream1Err = router.SubscribeToEvents(identityKey, stream1)
	}()

	go func() {
		defer wg.Done()
		stream2Err = router.SubscribeToEvents(identityKey, stream2)
	}()

	time.Sleep(200 * time.Millisecond)

	signingKeyshare, err := dbClient.SigningKeyshare.Create().
		SetStatus(schematype.KeyshareStatusAvailable).
		SetSecretShare([]byte("test-secret-share")).
		SetPublicShares(map[string][]byte{"so1": []byte("public-key-1")}).
		SetPublicKey([]byte("test-public-key")).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(t.Context())
	require.NoError(t, err)

	depositAddr, err := dbClient.DepositAddress.Create().
		SetOwnerIdentityPubkey(identityKey).
		SetOwnerSigningPubkey(identityKey).
		SetSigningKeyshare(signingKeyshare).
		SetAddress("test-address").
		SetNodeID(uuid.New()).
		Save(t.Context())
	require.NoError(t, err)

	_, err = dbClient.DepositAddress.UpdateOneID(depositAddr.ID).
		SetConfirmationTxid("test-txid-123").
		Save(t.Context())
	require.NoError(t, err)

	timeout := time.After(5 * time.Second)
	var stream1Received, stream2Received bool

	for !stream1Received || !stream2Received {
		select {
		case <-timeout:
			t.Fatalf("Timeout waiting for notifications. stream1: %v, stream2: %v", stream1Received, stream2Received)
		case <-time.After(100 * time.Millisecond):
			// Check if both streams received messages
			stream1.mu.Lock()
			stream1Received = len(stream1.messages) > 0
			stream1.mu.Unlock()

			stream2.mu.Lock()
			stream2Received = len(stream2.messages) > 0
			stream2.mu.Unlock()

			if stream1Received && stream2Received {
				break
			}
		}
	}

	require.True(t, stream1Received, "Stream1 should have received notification")
	require.True(t, stream2Received, "Stream2 should have received notification")

	cancel1()
	cancel2()
	wg.Wait()

	require.NoError(t, stream1Err, "Stream1 should not have errored")
	require.NoError(t, stream2Err, "Stream2 should not have errored")
}
