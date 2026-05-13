package events

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/entexample"
	"github.com/lightsparkdev/spark/so/ent/eventmessage"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/grpc/grpcutil"
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

	router := NewEventRouter(t.Context(), dbClient, dbEvents, zaptest.NewLogger(t).With(zap.String("component", "events_router")), &so.Config{})
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

	router := NewEventRouter(t.Context(), dbClient, dbEvents, zaptest.NewLogger(t).With(zap.String("component", "events_router")), &so.Config{})
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
	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, &so.Config{})
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
	sessionFactory := db.NewDefaultSessionFactory(dbClient)
	session := sessionFactory.NewSession(t.Context())
	mutationCtx := ent.InjectNotifier(ent.Inject(t.Context(), session), session)
	tx, err := session.GetOrBeginTx(mutationCtx)
	require.NoError(t, err)
	transfer, err := tx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetStatus(schematype.TransferStatusSenderKeyTweaked).
		SetType(schematype.TransferTypeTransfer).
		SetExpiryTime(expiry).
		SetTotalValue(100).
		Save(mutationCtx)
	require.NoError(t, err)

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
			if msg.GetReceiverTransfer() != nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "expected transfer notification")

	stream.mu.Lock()
	defer stream.mu.Unlock()
	var transferEvent *pb.SubscribeToEventsResponse
	for _, msg := range stream.messages {
		if msg.GetReceiverTransfer() != nil {
			transferEvent = msg
			break
		}
	}
	require.NotNil(t, transferEvent, "expected transfer event")

	receivedTransfer := transferEvent.GetReceiverTransfer().GetTransfer()
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

	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, cfg)
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

	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, cfg)
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

func TestEventRouter_SendsHeartbeatWhileIdle(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	cfg := sparktesting.TestConfig(t)
	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, cfg)
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobGrpcServerStreamHeartbeatEnabled: 100,
	})
	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	originalInterval := streamHeartbeatInterval
	streamHeartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		streamHeartbeatInterval = originalInterval
	})

	streamCtx, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
	defer cancel()
	streamCtx = knobs.InjectKnobsService(streamCtx, fixedKnobs)

	stream := &mockStream{ctx: streamCtx, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	err := router.SubscribeToEvents(identityKey, stream)
	require.NoError(t, err)

	stream.mu.Lock()
	defer stream.mu.Unlock()

	require.GreaterOrEqual(t, len(stream.messages), 2)
	require.NotNil(t, stream.messages[0].GetConnected())
	for _, message := range stream.messages[1:] {
		require.NotNil(t, message.GetHeartbeat())
	}
}

func TestEventRouter_DoesNotSendHeartbeatWhenDisabled(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	cfg := sparktesting.TestConfig(t)
	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, cfg)
	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	originalInterval := streamHeartbeatInterval
	streamHeartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		streamHeartbeatInterval = originalInterval
	})

	streamCtx, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
	defer cancel()

	stream := &mockStream{ctx: streamCtx, messages: make([]*pb.SubscribeToEventsResponse, 0)}

	err := router.SubscribeToEvents(identityKey, stream)
	require.NoError(t, err)

	stream.mu.Lock()
	defer stream.mu.Unlock()

	require.Len(t, stream.messages, 1)
	require.NotNil(t, stream.messages[0].GetConnected())
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

	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, cfg)
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

	st, ok := status.FromError(err)
	require.True(t, ok, "Error should be a gRPC status error")
	require.Equal(t, codes.PermissionDenied, st.Code(), "Error should be PERMISSION_DENIED")

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

	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, cfg)
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

	st, ok := status.FromError(err)
	require.True(t, ok, "Error should be a gRPC status error")
	require.Equal(t, codes.PermissionDenied, st.Code(), "Error should be PERMISSION_DENIED")

	stream.mu.Lock()
	require.Empty(t, stream.messages, "Stream should not have received any messages")
	stream.mu.Unlock()
}

func TestEventRouter_TransferNotifications(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, &so.Config{})
	rng := rand.NewChaCha8([32]byte{})

	senderKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	thirdPartyKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	selfTransferKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create and subscribe all streams
	senderStreamCtx, senderCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer senderCancel()
	senderStream := &mockStream{ctx: senderStreamCtx}

	receiverStreamCtx, receiverCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer receiverCancel()
	receiverStream := &mockStream{ctx: receiverStreamCtx}

	thirdPartyStreamCtx, thirdPartyCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer thirdPartyCancel()
	thirdPartyStream := &mockStream{ctx: thirdPartyStreamCtx}

	selfTransferStreamCtx, selfTransferCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer selfTransferCancel()
	selfTransferStream := &mockStream{ctx: selfTransferStreamCtx}

	errCh := make(chan error, 4)
	go func() { errCh <- router.SubscribeToEvents(senderKey, senderStream) }()
	go func() { errCh <- router.SubscribeToEvents(receiverKey, receiverStream) }()
	go func() { errCh <- router.SubscribeToEvents(thirdPartyKey, thirdPartyStream) }()
	go func() { errCh <- router.SubscribeToEvents(selfTransferKey, selfTransferStream) }()

	time.Sleep(200 * time.Millisecond)

	sessionFactory := db.NewDefaultSessionFactory(dbClient)

	// Helper to get statuses from stream
	getSenderStatuses := func(stream *mockStream) []pb.TransferStatus {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		var result []pb.TransferStatus
		for _, msg := range stream.messages {
			if msg.GetSenderTransfer() != nil {
				result = append(result, msg.GetSenderTransfer().GetTransfer().GetStatus())
			}
		}
		return result
	}

	getReceiverStatuses := func(stream *mockStream) []pb.TransferStatus {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		var result []pb.TransferStatus
		for _, msg := range stream.messages {
			if msg.GetReceiverTransfer() != nil {
				result = append(result, msg.GetReceiverTransfer().GetTransfer().GetStatus())
			}
		}
		return result
	}

	// Statuses that should trigger sender notifications
	senderNotifyStatuses := []schematype.TransferStatus{
		schematype.TransferStatusSenderInitiated,
		schematype.TransferStatusSenderInitiatedCoordinator,
		schematype.TransferStatusSenderKeyTweakPending,
		schematype.TransferStatusReturned,
		schematype.TransferStatusSenderKeyTweaked, // Also triggers receiver
	}

	// Unhandled statuses (from Values() minus handled)
	handledStatuses := map[schematype.TransferStatus]bool{
		schematype.TransferStatusSenderInitiated:            true,
		schematype.TransferStatusSenderInitiatedCoordinator: true,
		schematype.TransferStatusSenderKeyTweakPending:      true,
		schematype.TransferStatusReturned:                   true,
		schematype.TransferStatusSenderKeyTweaked:           true,
	}
	var unhandledStatuses []schematype.TransferStatus
	for _, s := range schematype.TransferStatus("").Values() {
		status := schematype.TransferStatus(s)
		if !handledStatuses[status] {
			unhandledStatuses = append(unhandledStatuses, status)
		}
	}

	statusToProto := map[schematype.TransferStatus]pb.TransferStatus{
		schematype.TransferStatusSenderInitiated:            pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
		schematype.TransferStatusSenderInitiatedCoordinator: pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
		schematype.TransferStatusSenderKeyTweakPending:      pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
		schematype.TransferStatusReturned:                   pb.TransferStatus_TRANSFER_STATUS_RETURNED,
		schematype.TransferStatusSenderKeyTweaked:           pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
	}

	containsStatus := func(statuses []pb.TransferStatus, want pb.TransferStatus) bool {
		return slices.Contains(statuses, want)
	}

	uniqueStatuses := func(statuses []pb.TransferStatus) []pb.TransferStatus {
		seen := make(map[pb.TransferStatus]struct{}, len(statuses))
		unique := make([]pb.TransferStatus, 0, len(statuses))
		for _, status := range statuses {
			if _, ok := seen[status]; ok {
				continue
			}
			seen[status] = struct{}{}
			unique = append(unique, status)
		}
		return unique
	}

	waitForStatus := func(name string, stream *mockStream, getStatuses func(*mockStream) []pb.TransferStatus, want pb.TransferStatus) {
		t.Helper()
		require.Eventuallyf(t, func() bool {
			return containsStatus(getStatuses(stream), want)
		}, 5*time.Second, 50*time.Millisecond, "expected %s status %v", name, want)
	}

	// Create main transfer and update through all statuses
	session := sessionFactory.NewSession(t.Context())
	mutationCtx := ent.InjectNotifier(ent.Inject(t.Context(), session), session)
	tx, err := session.GetOrBeginTx(mutationCtx)
	require.NoError(t, err)

	expiry := time.Now().Add(5 * time.Minute)
	transfer, err := tx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeTransfer).
		SetExpiryTime(expiry).
		SetTotalValue(100).
		Save(mutationCtx)
	require.NoError(t, err)

	// Create self-transfer
	selfTransfer, err := tx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetSenderIdentityPubkey(selfTransferKey).
		SetReceiverIdentityPubkey(selfTransferKey).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeTransfer).
		SetExpiryTime(expiry).
		SetTotalValue(100).
		Save(mutationCtx)
	require.NoError(t, err)

	require.NoError(t, tx.Commit())

	// Wait for the initial sender notifications before advancing statuses so the
	// router snapshots the expected state for each event.
	waitForStatus("sender initiated (sender)", senderStream, getSenderStatuses, statusToProto[schematype.TransferStatusSenderInitiated])
	waitForStatus("sender initiated (self)", selfTransferStream, getSenderStatuses, statusToProto[schematype.TransferStatusSenderInitiated])

	// Update main transfer through remaining handled statuses
	for _, status := range senderNotifyStatuses[1:] { // Skip first (already created with it)
		session := sessionFactory.NewSession(t.Context())
		mutationCtx := ent.InjectNotifier(ent.Inject(t.Context(), session), session)
		tx, err := session.GetOrBeginTx(mutationCtx)
		require.NoError(t, err)

		_, err = tx.Transfer.UpdateOneID(transfer.ID).
			SetStatus(status).
			Save(mutationCtx)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		protoStatus := statusToProto[status]
		waitForStatus("sender update", senderStream, getSenderStatuses, protoStatus)
		if status == schematype.TransferStatusSenderKeyTweaked {
			waitForStatus("receiver update", receiverStream, getReceiverStatuses, protoStatus)
		}
	}

	// Update self-transfer to SenderKeyTweaked
	session2 := sessionFactory.NewSession(t.Context())
	mutationCtx2 := ent.InjectNotifier(ent.Inject(t.Context(), session2), session2)
	tx2, err := session2.GetOrBeginTx(mutationCtx2)
	require.NoError(t, err)
	_, err = tx2.Transfer.UpdateOneID(selfTransfer.ID).
		SetStatus(schematype.TransferStatusSenderKeyTweaked).
		Save(mutationCtx2)
	require.NoError(t, err)
	require.NoError(t, tx2.Commit())
	waitForStatus("self sender key tweaked (sender)", selfTransferStream, getSenderStatuses, statusToProto[schematype.TransferStatusSenderKeyTweaked])
	waitForStatus("self sender key tweaked (receiver)", selfTransferStream, getReceiverStatuses, statusToProto[schematype.TransferStatusSenderKeyTweaked])

	// Update main transfer through unhandled statuses (should NOT notify)
	for _, status := range unhandledStatuses {
		session := sessionFactory.NewSession(t.Context())
		mutationCtx := ent.InjectNotifier(ent.Inject(t.Context(), session), session)
		tx, err := session.GetOrBeginTx(mutationCtx)
		require.NoError(t, err)

		_, err = tx.Transfer.UpdateOneID(transfer.ID).
			SetStatus(status).
			Save(mutationCtx)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}
	time.Sleep(300 * time.Millisecond)

	// Expected statuses
	expectedSenderStatuses := []pb.TransferStatus{
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
		pb.TransferStatus_TRANSFER_STATUS_RETURNED,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
	}

	// Verify exact statuses
	t.Run("sender receives correct statuses", func(t *testing.T) {
		require.ElementsMatch(t, expectedSenderStatuses, getSenderStatuses(senderStream))
	})

	t.Run("receiver only receives SenderKeyTweaked", func(t *testing.T) {
		require.Equal(t, []pb.TransferStatus{
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		}, getReceiverStatuses(receiverStream))
	})

	t.Run("third party receives nothing", func(t *testing.T) {
		require.Empty(t, getSenderStatuses(thirdPartyStream))
		require.Empty(t, getReceiverStatuses(thirdPartyStream))
	})

	t.Run("self-transfer receives both events with correct statuses", func(t *testing.T) {
		selfSender := uniqueStatuses(getSenderStatuses(selfTransferStream))
		selfReceiver := uniqueStatuses(getReceiverStatuses(selfTransferStream))
		require.ElementsMatch(t, []pb.TransferStatus{
			pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		}, selfSender)
		require.Equal(t, []pb.TransferStatus{
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		}, selfReceiver)
	})

	// Cleanup
	senderCancel()
	receiverCancel()
	thirdPartyCancel()
	selfTransferCancel()
	for range 4 {
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("router did not exit after cancel")
		}
	}
}

func TestEventRouter_MIMOFanOutNotifications(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, &so.Config{})
	rng := rand.NewChaCha8([32]byte{1}) // Different seed from other tests

	senderKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	primaryReceiverKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secondaryReceiverKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	nonReceiverKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Subscribe all streams.
	senderStreamCtx, senderCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer senderCancel()
	senderStream := &mockStream{ctx: senderStreamCtx}

	primaryStreamCtx, primaryCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer primaryCancel()
	primaryStream := &mockStream{ctx: primaryStreamCtx}

	secondaryStreamCtx, secondaryCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer secondaryCancel()
	secondaryStream := &mockStream{ctx: secondaryStreamCtx}

	nonReceiverStreamCtx, nonReceiverCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer nonReceiverCancel()
	nonReceiverStream := &mockStream{ctx: nonReceiverStreamCtx}

	errCh := make(chan error, 4)
	go func() { errCh <- router.SubscribeToEvents(senderKey, senderStream) }()
	go func() { errCh <- router.SubscribeToEvents(primaryReceiverKey, primaryStream) }()
	go func() { errCh <- router.SubscribeToEvents(secondaryReceiverKey, secondaryStream) }()
	go func() { errCh <- router.SubscribeToEvents(nonReceiverKey, nonReceiverStream) }()

	time.Sleep(200 * time.Millisecond)

	sessionFactory := db.NewDefaultSessionFactory(dbClient)

	getReceiverStatuses := func(stream *mockStream) []pb.TransferStatus {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		var result []pb.TransferStatus
		for _, msg := range stream.messages {
			if msg.GetReceiverTransfer() != nil {
				result = append(result, msg.GetReceiverTransfer().GetTransfer().GetStatus())
			}
		}
		return result
	}

	getSenderStatuses := func(stream *mockStream) []pb.TransferStatus {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		var result []pb.TransferStatus
		for _, msg := range stream.messages {
			if msg.GetSenderTransfer() != nil {
				result = append(result, msg.GetSenderTransfer().GetTransfer().GetStatus())
			}
		}
		return result
	}

	// Step 1: Create the transfer at SenderInitiated.
	expiry := time.Now().Add(5 * time.Minute)
	session := sessionFactory.NewSession(t.Context())
	mutationCtx := ent.InjectNotifier(ent.Inject(t.Context(), session), session)
	tx, err := session.GetOrBeginTx(mutationCtx)
	require.NoError(t, err)

	transfer, err := tx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(primaryReceiverKey).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeTransfer).
		SetExpiryTime(expiry).
		SetTotalValue(100).
		Save(mutationCtx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Wait for sender to receive the SenderInitiated event before proceeding.
	require.Eventually(t, func() bool {
		return slices.Contains(getSenderStatuses(senderStream), pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED)
	}, 5*time.Second, 50*time.Millisecond, "expected sender to get SenderInitiated")

	// Step 2: Create TransferReceiver entries (primary + secondary).
	// These must exist before the status update so the fan-out hook can query them.
	_, err = dbClient.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(primaryReceiverKey).
		SetStatus(schematype.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(t.Context())
	require.NoError(t, err)

	_, err = dbClient.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(secondaryReceiverKey).
		SetStatus(schematype.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(t.Context())
	require.NoError(t, err)

	// Step 3: Update transfer to SenderKeyTweaked. This triggers the fan-out hook
	// which emits additional events for each secondary receiver.
	session2 := sessionFactory.NewSession(t.Context())
	mutationCtx2 := ent.InjectNotifier(ent.Inject(t.Context(), session2), session2)
	tx2, err := session2.GetOrBeginTx(mutationCtx2)
	require.NoError(t, err)

	_, err = tx2.Transfer.UpdateOneID(transfer.ID).
		SetStatus(schematype.TransferStatusSenderKeyTweaked).
		Save(mutationCtx2)
	require.NoError(t, err)
	require.NoError(t, tx2.Commit())

	// Wait for the secondary receiver to get the event — this is the core assertion.
	require.Eventually(t, func() bool {
		return slices.Contains(
			getReceiverStatuses(secondaryStream),
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		)
	}, 5*time.Second, 50*time.Millisecond, "secondary receiver should get SenderKeyTweaked via fan-out")

	// Also wait for primary receiver.
	require.Eventually(t, func() bool {
		return slices.Contains(
			getReceiverStatuses(primaryStream),
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		)
	}, 5*time.Second, 50*time.Millisecond, "primary receiver should get SenderKeyTweaked")

	// Allow extra time for any straggling events.
	time.Sleep(300 * time.Millisecond)

	t.Run("secondary receiver gets SenderKeyTweaked", func(t *testing.T) {
		require.Equal(t, []pb.TransferStatus{
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		}, getReceiverStatuses(secondaryStream))
	})

	t.Run("primary receiver gets exactly one SenderKeyTweaked (no double-notify)", func(t *testing.T) {
		require.Equal(t, []pb.TransferStatus{
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		}, getReceiverStatuses(primaryStream))
	})

	t.Run("sender gets exactly both statuses (no duplicate from fan-out)", func(t *testing.T) {
		require.ElementsMatch(t, []pb.TransferStatus{
			pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		}, getSenderStatuses(senderStream))
	})

	t.Run("non-receiver gets nothing", func(t *testing.T) {
		require.Empty(t, getSenderStatuses(nonReceiverStream))
		require.Empty(t, getReceiverStatuses(nonReceiverStream))
	})

	// Step 4: Advance to Completed — secondary receiver should NOT get a
	// fan-out event because the hook only fires for SenderKeyTweaked.
	session3 := sessionFactory.NewSession(t.Context())
	mutationCtx3 := ent.InjectNotifier(ent.Inject(t.Context(), session3), session3)
	tx3, err := session3.GetOrBeginTx(mutationCtx3)
	require.NoError(t, err)

	_, err = tx3.Transfer.UpdateOneID(transfer.ID).
		SetStatus(schematype.TransferStatusCompleted).
		Save(mutationCtx3)
	require.NoError(t, err)
	require.NoError(t, tx3.Commit())

	// Give time for any spurious events to arrive.
	time.Sleep(300 * time.Millisecond)

	t.Run("secondary receiver does NOT get Completed (fan-out only for SenderKeyTweaked)", func(t *testing.T) {
		require.Equal(t, []pb.TransferStatus{
			pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		}, getReceiverStatuses(secondaryStream), "secondary receiver should only have SenderKeyTweaked, not Completed")
	})

	// Cleanup
	senderCancel()
	primaryCancel()
	secondaryCancel()
	nonReceiverCancel()
	for range 4 {
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("router did not exit after cancel")
		}
	}
}

func TestEventRouter_TokenTransactionFanOut(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, &so.Config{})
	rng := rand.NewChaCha8([32]byte{42})

	// Enable the token tx events knob for this test.
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTxEventsEnabled: 100,
	})

	senderKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver2Key := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	selfTransferKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Subscribe all streams via the public SubscribeToEvents API.
	type testStream struct {
		stream *mockStream
		cancel context.CancelFunc
	}
	streams := make(map[string]*testStream)
	allKeys := map[string]keys.Public{
		"sender":       senderKey,
		"receiver":     receiverKey,
		"receiver2":    receiver2Key,
		"selfTransfer": selfTransferKey,
		"other":        otherKey,
	}
	errCh := make(chan error, len(allKeys))
	for name, key := range allKeys {
		streamCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		streamCtx = knobs.InjectKnobsService(streamCtx, fixedKnobs)
		defer cancel()
		s := &mockStream{ctx: streamCtx}
		streams[name] = &testStream{stream: s, cancel: cancel}
		go func(k keys.Public, st *mockStream) {
			errCh <- router.SubscribeToEvents(k, st)
		}(key, s)
		_ = name
	}

	time.Sleep(200 * time.Millisecond)

	sessionFactory := db.NewDefaultSessionFactory(dbClient)

	createKeyshare := func() *ent.SigningKeyshare {
		return entexample.NewSigningKeyshareExample(t, dbClient).
			SetStatus(schematype.KeyshareStatusAvailable).
			SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			MustExec(t.Context())
	}

	getTokenTxHashes := func(stream *mockStream) [][]byte {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		var hashes [][]byte
		for _, msg := range stream.messages {
			if msg.GetTokenTransaction() != nil {
				hashes = append(hashes, msg.GetTokenTransaction().GetTokenTransactionHash())
			}
		}
		return hashes
	}

	// finalizeTx creates a token transaction at STARTED, wires up the given
	// outputs, then transitions to FINALIZED via a notifier-enabled session.
	finalizeTx := func(t *testing.T, spentOwners []keys.Public, createdOwners []keys.Public) []byte {
		t.Helper()

		hash := make([]byte, 32)
		_, _ = rng.Read(hash)

		issuerSig := make([]byte, 64)
		_, _ = rng.Read(issuerSig)
		tokenID := make([]byte, 32)
		_, _ = rng.Read(tokenID)
		tokenCreate := entexample.NewTokenCreateExample(t, dbClient).
			SetIssuerSignature(issuerSig).
			SetTokenIdentifier(tokenID).
			MustExec(t.Context())
		mint := entexample.NewTokenMintExample(t, dbClient).MustExec(t.Context())
		opSig := make([]byte, 64)
		_, _ = rng.Read(opSig)
		tokenTx := entexample.NewTokenTransactionExample(t, dbClient).
			SetStatus(schematype.TokenTransactionStatusStarted).
			SetFinalizedTokenTransactionHash(hash).
			SetOperatorSignature(opSig).
			SetMint(mint).
			MustExec(t.Context())

		vout := int32(0)
		for _, owner := range spentOwners {
			fHash := make([]byte, 32)
			_, _ = rng.Read(fHash)
			entexample.NewTokenOutputExample(t, dbClient).
				SetOwnerPublicKey(owner).
				SetCreatedTransactionOutputVout(vout).
				SetCreatedTransactionFinalizedHash(fHash).
				SetOutputCreatedTokenTransaction(tokenTx).
				SetOutputSpentTokenTransaction(tokenTx).
				SetTokenCreate(tokenCreate).
				SetRevocationKeyshare(createKeyshare()).
				MustExec(t.Context())
			vout++
		}
		for _, owner := range createdOwners {
			fHash := make([]byte, 32)
			_, _ = rng.Read(fHash)
			entexample.NewTokenOutputExample(t, dbClient).
				SetOwnerPublicKey(owner).
				SetCreatedTransactionOutputVout(vout).
				SetCreatedTransactionFinalizedHash(fHash).
				SetOutputCreatedTokenTransaction(tokenTx).
				SetTokenCreate(tokenCreate).
				SetRevocationKeyshare(createKeyshare()).
				MustExec(t.Context())
			vout++
		}

		session := sessionFactory.NewSession(t.Context())
		mutationCtx := knobs.InjectKnobsService(
			ent.InjectNotifier(ent.Inject(t.Context(), session), session),
			fixedKnobs,
		)
		tx, err := session.GetOrBeginTx(mutationCtx)
		require.NoError(t, err)

		_, err = tx.TokenTransaction.UpdateOneID(tokenTx.ID).
			SetStatus(schematype.TokenTransactionStatusFinalized).
			Save(mutationCtx)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		return hash
	}

	waitForHash := func(t *testing.T, name string, stream *mockStream, hash []byte) {
		t.Helper()
		require.Eventuallyf(t, func() bool {
			for _, h := range getTokenTxHashes(stream) {
				if slices.Equal(h, hash) {
					return true
				}
			}
			return false
		}, 5*time.Second, 50*time.Millisecond, "%s should receive token tx hash", name)
	}

	hasHash := func(stream *mockStream, hash []byte) bool {
		for _, h := range getTokenTxHashes(stream) {
			if slices.Equal(h, hash) {
				return true
			}
		}
		return false
	}

	countHash := func(stream *mockStream, hash []byte) int {
		count := 0
		for _, h := range getTokenTxHashes(stream) {
			if slices.Equal(h, hash) {
				count++
			}
		}
		return count
	}

	// ---- Test case 1: receiver-only (mint/create — only created outputs) ----
	t.Run("receiver only (no spent outputs)", func(t *testing.T) {
		hash := finalizeTx(t,
			nil,                        // no spent outputs
			[]keys.Public{receiverKey}, // one created output
		)
		waitForHash(t, "receiver", streams["receiver"].stream, hash)

		time.Sleep(300 * time.Millisecond)
		for _, name := range []string{"sender", "receiver2", "selfTransfer", "other"} {
			require.False(t, hasHash(streams[name].stream, hash),
				"%s should NOT receive receiver-only tx", name)
		}
	})

	// ---- Test case 2: transfer with change output back to sender ----
	t.Run("transfer with change output", func(t *testing.T) {
		hash := finalizeTx(t,
			[]keys.Public{senderKey},              // sender's input
			[]keys.Public{receiverKey, senderKey}, // receiver + change
		)
		waitForHash(t, "sender", streams["sender"].stream, hash)
		waitForHash(t, "receiver", streams["receiver"].stream, hash)

		time.Sleep(300 * time.Millisecond)
		for _, name := range []string{"receiver2", "selfTransfer", "other"} {
			require.False(t, hasHash(streams[name].stream, hash),
				"%s should NOT receive sender→receiver tx", name)
		}
	})

	// ---- Test case 3: MIMO — multiple receivers ----
	t.Run("MIMO multiple receivers", func(t *testing.T) {
		hash := finalizeTx(t,
			[]keys.Public{senderKey},                            // sender's input
			[]keys.Public{receiverKey, receiver2Key, senderKey}, // two receivers + change
		)
		waitForHash(t, "sender", streams["sender"].stream, hash)
		waitForHash(t, "receiver", streams["receiver"].stream, hash)
		waitForHash(t, "receiver2", streams["receiver2"].stream, hash)

		time.Sleep(300 * time.Millisecond)
		for _, name := range []string{"selfTransfer", "other"} {
			require.False(t, hasHash(streams[name].stream, hash),
				"%s should NOT receive MIMO tx", name)
		}
	})

	// ---- Test case 4: self-transfer (sender == receiver) ----
	t.Run("self-transfer deduplicates", func(t *testing.T) {
		hash := finalizeTx(t,
			[]keys.Public{selfTransferKey},                  // spent by self
			[]keys.Public{selfTransferKey, selfTransferKey}, // two outputs back to self
		)
		waitForHash(t, "selfTransfer", streams["selfTransfer"].stream, hash)

		// Should receive exactly ONE notification despite appearing on 3 outputs.
		time.Sleep(300 * time.Millisecond)
		require.Equal(t, 1, countHash(streams["selfTransfer"].stream, hash),
			"self-transfer should produce exactly one notification")

		for _, name := range []string{"sender", "receiver", "receiver2", "other"} {
			require.False(t, hasHash(streams[name].stream, hash),
				"%s should NOT receive self-transfer tx", name)
		}
	})

	// Cleanup
	for _, ts := range streams {
		ts.cancel()
	}
	for range len(allKeys) {
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("router did not exit after cancel")
		}
	}
}

func TestEventRouter_TokenTransactionKnobDisabled(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	router := NewEventRouter(t.Context(), dbClient, dbEvents, logger, &so.Config{})
	rng := rand.NewChaCha8([32]byte{99})

	// Knob is OFF (default 0) — no token events should be delivered.
	disabledKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenTxEventsEnabled: 0,
	})

	senderKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	senderStreamCtx, senderCancel := context.WithTimeout(t.Context(), 15*time.Second)
	senderStreamCtx = knobs.InjectKnobsService(senderStreamCtx, disabledKnobs)
	defer senderCancel()
	senderStream := &mockStream{ctx: senderStreamCtx}

	receiverStreamCtx, receiverCancel := context.WithTimeout(t.Context(), 15*time.Second)
	receiverStreamCtx = knobs.InjectKnobsService(receiverStreamCtx, disabledKnobs)
	defer receiverCancel()
	receiverStream := &mockStream{ctx: receiverStreamCtx}

	errCh := make(chan error, 2)
	go func() { errCh <- router.SubscribeToEvents(senderKey, senderStream) }()
	go func() { errCh <- router.SubscribeToEvents(receiverKey, receiverStream) }()

	time.Sleep(200 * time.Millisecond)

	// Create and finalize a token transaction (with knob disabled in mutation ctx too).
	hash := make([]byte, 32)
	_, _ = rng.Read(hash)
	issuerSig := make([]byte, 64)
	_, _ = rng.Read(issuerSig)
	tokenID := make([]byte, 32)
	_, _ = rng.Read(tokenID)
	opSig := make([]byte, 64)
	_, _ = rng.Read(opSig)

	tokenCreate := entexample.NewTokenCreateExample(t, dbClient).
		SetIssuerSignature(issuerSig).
		SetTokenIdentifier(tokenID).
		MustExec(t.Context())
	mint := entexample.NewTokenMintExample(t, dbClient).MustExec(t.Context())
	tokenTx := entexample.NewTokenTransactionExample(t, dbClient).
		SetStatus(schematype.TokenTransactionStatusStarted).
		SetFinalizedTokenTransactionHash(hash).
		SetOperatorSignature(opSig).
		SetMint(mint).
		MustExec(t.Context())

	createKeyshare := func() *ent.SigningKeyshare {
		return entexample.NewSigningKeyshareExample(t, dbClient).
			SetStatus(schematype.KeyshareStatusAvailable).
			SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			MustExec(t.Context())
	}

	fHash1 := make([]byte, 32)
	_, _ = rng.Read(fHash1)
	entexample.NewTokenOutputExample(t, dbClient).
		SetOwnerPublicKey(senderKey).
		SetCreatedTransactionOutputVout(0).
		SetCreatedTransactionFinalizedHash(fHash1).
		SetOutputCreatedTokenTransaction(tokenTx).
		SetOutputSpentTokenTransaction(tokenTx).
		SetTokenCreate(tokenCreate).
		SetRevocationKeyshare(createKeyshare()).
		MustExec(t.Context())

	fHash2 := make([]byte, 32)
	_, _ = rng.Read(fHash2)
	entexample.NewTokenOutputExample(t, dbClient).
		SetOwnerPublicKey(receiverKey).
		SetCreatedTransactionOutputVout(1).
		SetCreatedTransactionFinalizedHash(fHash2).
		SetOutputCreatedTokenTransaction(tokenTx).
		SetTokenCreate(tokenCreate).
		SetRevocationKeyshare(createKeyshare()).
		MustExec(t.Context())

	sessionFactory := db.NewDefaultSessionFactory(dbClient)
	session := sessionFactory.NewSession(t.Context())
	mutationCtx := knobs.InjectKnobsService(
		ent.InjectNotifier(ent.Inject(t.Context(), session), session),
		disabledKnobs,
	)
	tx, err := session.GetOrBeginTx(mutationCtx)
	require.NoError(t, err)

	_, err = tx.TokenTransaction.UpdateOneID(tokenTx.ID).
		SetStatus(schematype.TokenTransactionStatusFinalized).
		Save(mutationCtx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Wait long enough for events to propagate if they were going to.
	time.Sleep(500 * time.Millisecond)

	getTokenTxCount := func(stream *mockStream) int {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		count := 0
		for _, msg := range stream.messages {
			if msg.GetTokenTransaction() != nil {
				count++
			}
		}
		return count
	}

	require.Equal(t, 0, getTokenTxCount(senderStream), "sender should NOT receive token events when knob is disabled")
	require.Equal(t, 0, getTokenTxCount(receiverStream), "receiver should NOT receive token events when knob is disabled")

	senderCancel()
	receiverCancel()
	for range 2 {
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("router did not exit after cancel")
		}
	}
}

// waitForConnectedEvent blocks until the SubscribeToEvents handler has sent the
// initial Connected event, indicating it has entered its receive loop.
func waitForConnectedEvent(t *testing.T, stream *mockStream) {
	t.Helper()
	require.Eventually(t, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return len(stream.messages) > 0
	}, 2*time.Second, 10*time.Millisecond, "handler did not enter receive loop")
}

func TestSubscribeToEventsShutdownReturnsUnavailableForGrpcWeb(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	shutdownCtx, stopShutdown := context.WithCancel(t.Context())
	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	router := NewEventRouter(shutdownCtx, dbClient, dbEvents, logger, &so.Config{})

	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Production gRPC-web requests acquire this marker via MarkGrpcWebHandler.
	streamCtx := grpcutil.WithGrpcWebRequest(t.Context())
	stream := &mockStream{ctx: streamCtx}

	errCh := make(chan error, 1)
	go func() {
		errCh <- router.SubscribeToEvents(identityKey, stream)
	}()

	waitForConnectedEvent(t, stream)
	stopShutdown()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, sparkerrors.ErrShuttingDown)
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after shutdown signal")
	}
}

func TestSubscribeToEventsShutdownReturnsUnavailableForNativeGrpc(t *testing.T) {
	ctx, _, dbEvents := db.SetUpDBEventsTestContext(t)
	dbClient := ctx.Client

	shutdownCtx, stopShutdown := context.WithCancel(t.Context())
	logger := zaptest.NewLogger(t).With(zap.String("component", "events_router"))
	router := NewEventRouter(shutdownCtx, dbClient, dbEvents, logger, &so.Config{})

	rng := rand.NewChaCha8([32]byte{})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// No gRPC-web marker: simulates a request arriving via the native gRPC port.
	stream := &mockStream{ctx: t.Context()}

	errCh := make(chan error, 1)
	go func() {
		errCh <- router.SubscribeToEvents(identityKey, stream)
	}()

	waitForConnectedEvent(t, stream)
	stopShutdown()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, sparkerrors.ErrShuttingDown)
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after shutdown signal")
	}
}
