package events

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/eventmessage"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()

	m.Run()
}

type mockStream struct {
	ctx      context.Context
	messages []*pb.SubscribeToEventsResponse
	mu       sync.Mutex
	sendErr  error
}

func (m *mockStream) Send(msg *pb.SubscribeToEventsResponse) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

func (m *mockStream) RecvMsg(_ any) error {
	return nil
}

func (m *mockStream) Context() context.Context {
	return m.ctx
}

func (m *mockStream) SendHeader(_ metadata.MD) error {
	return nil
}

func (m *mockStream) SendMsg(_ any) error {
	return nil
}

func (m *mockStream) SetHeader(_ metadata.MD) error {
	return nil
}

func (m *mockStream) SetTrailer(_ metadata.MD) {}

func TestEventRouterConcurrency(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	router := NewEventRouter(dbClient, dbEvents, zaptest.NewLogger(t).With(zap.String("component", "events_router")), &so.Config{})
	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	const numGoroutines = 100
	var wg sync.WaitGroup

	makeStream := func(i int) *mockStream {
		switch i % 3 {
		case 0:
			// Normal stream
			ctx, cancel := context.WithCancel(t.Context())
			stream := &mockStream{ctx: ctx}

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
			stream.messages = nil
			return stream
		case 1:
			// Stream that errors on send
			return &mockStream{
				ctx:     t.Context(),
				sendErr: status.Error(codes.Unavailable, "stream closed"),
			}
		default:
			// Stream with cancellable context
			ctx, cancel := context.WithCancel(t.Context())
			stream := &mockStream{ctx: ctx}
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
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	router := NewEventRouter(dbClient, dbEvents, zaptest.NewLogger(t).With(zap.String("component", "events_router")), &so.Config{})
	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	ctx1, cancel1 := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel1()
	stream1 := &mockStream{ctx: ctx1, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel2()
	stream2 := &mockStream{ctx: ctx2, messages: make([]*pb.SubscribeToEventsResponse, 0)}

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

	secret := keys.MustGeneratePrivateKeyFromRand(rng)
	pubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingKeyshare, err := dbClient.SigningKeyshare.Create().
		SetStatus(schematype.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"so1": secret.Public()}).
		SetPublicKey(pubKey).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(t.Context())
	require.NoError(t, err)

	depositAddr, err := dbClient.DepositAddress.Create().
		SetOwnerIdentityPubkey(identityKey).
		SetOwnerSigningPubkey(identityKey).
		SetSigningKeyshare(signingKeyshare).
		SetAddress("test-address").
		SetNodeID(uuid.Must(uuid.NewRandomFromReader(rng))).
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

func TestEventRouterTransferNotification(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	router := NewEventRouter(dbClient, dbEvents, logger, &so.Config{})
	rng := rand.NewChaCha8([32]byte{})

	receiverKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	streamCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream := &mockStream{ctx: streamCtx}

	errCh := make(chan error, 1)
	go func() {
		errCh <- router.SubscribeToEvents(receiverKey, stream)
	}()

	// Give the router some time to register the listener.
	time.Sleep(200 * time.Millisecond)

	expiry := time.Now().Add(5 * time.Minute)
	sessionFactory := db.NewDefaultSessionFactory(dbClient, knobs.NewEmptyFixedKnobs())
	session := sessionFactory.NewSession(t.Context())
	mutationCtx := ent.InjectNotifier(ent.Inject(t.Context(), session), session)
	tx, err := session.GetOrBeginTx(mutationCtx)
	require.NoError(t, err)
	transfer, err := tx.Transfer.Create().
		SetNetwork(schematype.NetworkRegtest).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetStatus(schematype.TransferStatusSenderKeyTweaked).
		SetType(schematype.TransferTypeTransfer).
		SetExpiryTime(expiry).
		SetTotalValue(100).
		Save(mutationCtx)
	require.NoError(t, err)
	ent.MarkTxDirty(mutationCtx)

	require.NoError(t, tx.Commit())

	require.Eventually(t, func() bool {
		count, err := dbClient.EventMessage.Query().
			Where(eventmessage.Channel("transfer")).
			Count(t.Context())
		require.NoError(t, err)
		return count > 0
	}, time.Second, 50*time.Millisecond, "expected outbox entry")

	require.Eventually(t, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		for _, msg := range stream.messages {
			if msg.GetTransfer() != nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "expected transfer notification")

	stream.mu.Lock()
	defer stream.mu.Unlock()
	var transferEvent *pb.SubscribeToEventsResponse
	for _, msg := range stream.messages {
		if msg.GetTransfer() != nil {
			transferEvent = msg
			break
		}
	}
	require.NotNil(t, transferEvent, "expected transfer event")

	receivedTransfer := transferEvent.GetTransfer().GetTransfer()
	require.Equal(t, transfer.ID.String(), receivedTransfer.GetId())
	require.Equal(t, pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED, receivedTransfer.GetStatus())

	cancel()
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("router did not exit after cancel")
	}
}

func TestMasterWalletHasReadAccess(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	cfg := sparktesting.TestConfig(t)

	// Enable privacy knob
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100, // 100% rollout = always enabled
	})

	router := NewEventRouter(dbClient, dbEvents, logger, cfg)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key for wallet owner
	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Generate test identity public key for master
	masterPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create wallet setting with privacy enabled and master set
	_, err := dbClient.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		SetMasterIdentityPublicKey(masterPubKey).
		Save(t.Context())
	require.NoError(t, err)

	// Set up stream context with master session
	streamCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Inject knobs and session into stream context
	streamCtx = knobs.InjectKnobsService(streamCtx, fixedKnobs)
	streamCtx = authn.InjectSessionForTests(streamCtx, hex.EncodeToString(masterPubKey.Serialize()), 9999999999)

	stream := &mockStream{ctx: streamCtx, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	// Subscribe should succeed because master has access
	err = router.SubscribeToEvents(walletOwnerPubKey, stream)
	require.NoError(t, err, "Master wallet should have read access")

	// Verify that the stream received the connected event
	stream.mu.Lock()
	require.NotEmpty(t, stream.messages, "Stream should have received connected event")
	require.NotNil(t, stream.messages[0].GetConnected(), "First message should be connected event")
	stream.mu.Unlock()
}

func TestEventRouter_PrivacyEnabled_OwnerAccess(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	cfg := sparktesting.TestConfig(t)

	// Enable privacy knob
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100,
	})

	router := NewEventRouter(dbClient, dbEvents, logger, cfg)
	rng := rand.NewChaCha8([32]byte{})

	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create wallet setting with privacy enabled
	_, err := dbClient.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		Save(t.Context())
	require.NoError(t, err)

	// Set up stream context with owner session
	streamCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	streamCtx = knobs.InjectKnobsService(streamCtx, fixedKnobs)
	streamCtx = authn.InjectSessionForTests(streamCtx, hex.EncodeToString(walletOwnerPubKey.Serialize()), 9999999999)

	stream := &mockStream{ctx: streamCtx, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	// Subscribe should succeed because owner has access
	err = router.SubscribeToEvents(walletOwnerPubKey, stream)
	require.NoError(t, err, "Owner should have read access")

	stream.mu.Lock()
	require.NotEmpty(t, stream.messages, "Stream should have received connected event")
	stream.mu.Unlock()
}

func TestEventRouter_PrivacyEnabled_NoAccess(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	cfg := sparktesting.TestConfig(t)

	// Enable privacy knob
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100,
	})

	router := NewEventRouter(dbClient, dbEvents, logger, cfg)
	rng := rand.NewChaCha8([32]byte{})

	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherUserPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create wallet setting with privacy enabled
	_, err := dbClient.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		Save(t.Context())
	require.NoError(t, err)

	// Set up stream context with different user session (not owner, not master)
	streamCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	streamCtx = knobs.InjectKnobsService(streamCtx, fixedKnobs)
	streamCtx = authn.InjectSessionForTests(streamCtx, hex.EncodeToString(otherUserPubKey.Serialize()), 9999999999)

	stream := &mockStream{ctx: streamCtx, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	// Subscribe should fail because user doesn't have access
	err = router.SubscribeToEvents(walletOwnerPubKey, stream)
	require.Error(t, err, "Non-owner should not have read access")
	require.Contains(t, err.Error(), "user does not have read access to the wallet")

	// Stream should not have received any messages
	stream.mu.Lock()
	require.Empty(t, stream.messages, "Stream should not have received any messages")
	stream.mu.Unlock()
}

func TestEventRouter_PrivacyEnabled_NoSession(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	cfg := sparktesting.TestConfig(t)

	// Enable privacy knob
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100,
	})

	router := NewEventRouter(dbClient, dbEvents, logger, cfg)
	rng := rand.NewChaCha8([32]byte{})

	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create wallet setting with privacy enabled
	_, err := dbClient.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		Save(t.Context())
	require.NoError(t, err)

	// Set up stream context with knobs but NO session
	streamCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	streamCtx = knobs.InjectKnobsService(streamCtx, fixedKnobs)
	// Note: No authn session injected

	stream := &mockStream{ctx: streamCtx, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	// Subscribe should fail because there's no session
	err = router.SubscribeToEvents(walletOwnerPubKey, stream)
	require.Error(t, err, "Should fail without session")
	require.Contains(t, err.Error(), "user does not have read access to the wallet")

	stream.mu.Lock()
	require.Empty(t, stream.messages, "Stream should not have received any messages")
	stream.mu.Unlock()
}
