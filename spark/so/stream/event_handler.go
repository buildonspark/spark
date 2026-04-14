package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

const (
	eventNameDepositAddress   = "depositaddress"
	eventNameTransfer         = "transfer"
	eventNameTokenTransaction = "tokentransaction"
)

// streamHeartbeatInterval controls how often a HeartbeatEvent is sent to idle
// subscribers. The SDK-side inactivity timeout
// (STREAM_HEARTBEAT_TIMEOUT_MS in spark-wallet.ts) should stay at least 2x this
// value so one missed heartbeat does not trigger an unnecessary reconnect.
var streamHeartbeatInterval = 5 * time.Second

type EventRouter struct {
	dbEvents *db.DBEvents
	logger   *zap.Logger
	dbClient *ent.Client
	config   *so.Config
}

func NewEventRouter(dbClient *ent.Client, dbEvents *db.DBEvents, logger *zap.Logger, config *so.Config) *EventRouter {
	defaultRouter := &EventRouter{
		dbEvents: dbEvents,
		logger:   logger,
		dbClient: dbClient,
		config:   config,
	}

	return defaultRouter
}

func (s *EventRouter) SubscribeToEvents(identityPublicKey keys.Public, stream pb.SparkService_SubscribeToEventsServer) error {
	readCtx := stream.Context()
	readOnlySession := db.NewReadOnlySession(readCtx, s.dbClient)
	readCtx = ent.Inject(readCtx, readOnlySession)
	knobsService := knobs.GetKnobsService(stream.Context())

	walletSettingHandler := handler.NewWalletSettingHandler(s.config)
	hasReadAccess, err := walletSettingHandler.HasReadAccessToWallet(readCtx, identityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to check read access: %w", err)
	}
	if !hasReadAccess {
		return sparkerrors.PermissionDeniedNoReadAccess(fmt.Errorf("user does not have read access to the wallet"))
	}

	notificationChan, cleanup := s.createNotificationChannel(stream.Context(), identityPublicKey)
	defer cleanup()

	connectedEvent := &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_Connected{
			Connected: &pb.ConnectedEvent{},
		},
	}
	if err := stream.Send(connectedEvent); err != nil {
		return nil
	}
	// Default off so the SDK-side listener can land before heartbeats are
	// rolled out on coordinators.
	heartbeatEnabled := knobsService.GetValue(
		knobs.KnobGrpcServerStreamHeartbeatEnabled,
		0,
	) > 0
	var heartbeatTicker *time.Ticker
	var heartbeatEvents <-chan time.Time
	if heartbeatEnabled {
		heartbeatTicker = time.NewTicker(streamHeartbeatInterval)
		heartbeatEvents = heartbeatTicker.C
		defer heartbeatTicker.Stop()
	}
	heartbeatEvent := &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_Heartbeat{
			Heartbeat: &pb.HeartbeatEvent{},
		},
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-heartbeatEvents:
			if err := stream.Send(heartbeatEvent); err != nil {
				return nil
			}
		case eventData, ok := <-notificationChan:
			if !ok {
				return nil
			}

			notifications, err := s.processNotification(stream.Context(), eventData, identityPublicKey)

			if err != nil {
				s.logger.With(zap.Error(err)).Error("Failed to process notification")
			} else {
				for _, notification := range notifications {
					if err := stream.Send(notification); err != nil {
						return nil
					}
				}
			}
		}
	}
}

func (s *EventRouter) createNotificationChannel(ctx context.Context, identityPublicKey keys.Public) (chan db.EventData, func()) {
	subscriptions := []db.Subscription{
		{
			EventName: eventNameDepositAddress,
			Field:     depositaddress.FieldOwnerIdentityPubkey,
			Value:     identityPublicKey.String(),
		},
		{
			EventName: eventNameTransfer,
			Field:     transfer.FieldReceiverIdentityPubkey,
			Value:     identityPublicKey.String(),
		},
		{
			EventName: eventNameTransfer,
			Field:     transfer.FieldSenderIdentityPubkey,
			Value:     identityPublicKey.String(),
		},
	}

	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobTokenTxEventsEnabled, 0) > 0 {
		subscriptions = append(subscriptions, db.Subscription{
			EventName: eventNameTokenTransaction,
			Field:     "owner_public_key",
			Value:     identityPublicKey.String(),
		})
	}

	notificationChan, cleanup := s.dbEvents.AddListeners(subscriptions)
	return notificationChan, cleanup
}

type processEventPayload struct {
	ID     uuid.UUID
	Fields map[string]any
}

func (s *EventRouter) processNotification(ctx context.Context, eventData db.EventData, identityPublicKey keys.Public) ([]*pb.SubscribeToEventsResponse, error) {
	var eventJson map[string]any
	err := json.Unmarshal([]byte(eventData.Payload), &eventJson)
	if err != nil {
		s.logger.With(zap.Error(err)).Error("Failed to unmarshal event data")
		return nil, err
	}

	idStr, ok := eventJson["id"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to parse ID as string")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		s.logger.With(zap.Error(err)).Error("Failed to parse ID as UUID")
		return nil, err
	}

	delete(eventJson, "id")

	event := processEventPayload{
		ID:     id,
		Fields: eventJson,
	}

	var notifications []*pb.SubscribeToEventsResponse
	switch eventData.Channel {
	case eventNameDepositAddress:
		if notification := s.processDepositNotification(ctx, event); notification != nil {
			notifications = append(notifications, notification)
		}
	case eventNameTransfer:
		notifications = s.processTransferNotification(ctx, event, identityPublicKey)
	case eventNameTokenTransaction:
		if notification := s.processTokenTransactionNotification(ctx, event); notification != nil {
			notifications = append(notifications, notification)
		}
	default:
		return nil, fmt.Errorf("unknown event type: %s", eventData.Channel)
	}

	return notifications, nil
}

func (s *EventRouter) processDepositNotification(ctx context.Context, event processEventPayload) *pb.SubscribeToEventsResponse {
	_, exists := event.Fields["confirmation_txid"]
	if !exists {
		return nil
	}

	// Always check availability_confirmed_at to avoid duplicate events.
	// Only send the deposit event when availability is actually confirmed.
	val, exists := event.Fields["availability_confirmed_at"]
	if !exists {
		return nil
	}

	// availability_confirmed_at is serialized as an RFC3339 string in the JSON payload
	// Check if it's the zero time value (0001-01-01T00:00:00Z)
	if timeStr, ok := val.(string); ok {
		t, err := time.Parse(time.RFC3339, timeStr)
		if err != nil {
			s.logger.With(zap.Error(err)).Sugar().Errorf("failed to parse availability_confirmed_at '%s' as time", timeStr)
			return nil
		}
		if t.IsZero() {
			return nil
		}
	} else {
		// Unexpected type - log and skip
		s.logger.Sugar().Errorf("availability_confirmed_at expected to be a string, but it was %T", val)
		return nil
	}

	depositAddress, err := s.dbClient.DepositAddress.Query().Where(depositaddress.ID(event.ID)).Only(ctx)
	if err != nil {
		return nil
	}
	if depositAddress.NodeID == uuid.Nil {
		// The comment below implies that this is safe to ignore
		return nil
	}

	treeNode, err := s.dbClient.TreeNode.Query().Where(treenode.ID(depositAddress.NodeID)).Only(ctx)
	if err != nil {
		// TODO: Fine to silently ignore this
		// If tree node doesn't exist maybe we can inform client that they can claim the deposit?
		return nil
	}

	treeNodeProto, err := treeNode.MarshalSparkProto(ctx)
	if err != nil {
		return nil
	}

	return &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_Deposit{
			Deposit: &pb.DepositEvent{
				Deposit: treeNodeProto,
			},
		},
	}
}

func (s *EventRouter) processTransferNotification(ctx context.Context, event processEventPayload, identityPublicKey keys.Public) []*pb.SubscribeToEventsResponse {
	if rawStatus, exists := event.Fields["status"]; exists {
		statusStr, ok := rawStatus.(string)
		if !ok {
			return nil
		}
		status := schematype.TransferStatus(statusStr)

		// These fields may be absent in fan-out events that target only one
		// side (e.g. MIMO receiver fan-out omits sender_identity_pubkey).
		// Treat a missing field as empty so it simply won't match.
		senderPubkey, senderOk := event.Fields["sender_identity_pubkey"].(string)
		receiverPubkey, receiverOk := event.Fields["receiver_identity_pubkey"].(string)
		subscriptionPubkey := identityPublicKey.String()

		var notifications []*pb.SubscribeToEventsResponse

		switch status {
		case schematype.TransferStatusSenderInitiated,
			schematype.TransferStatusSenderInitiatedCoordinator,
			schematype.TransferStatusSenderKeyTweakPending,
			schematype.TransferStatusReturned:
			if senderOk && senderPubkey == subscriptionPubkey {
				if notification := s.buildTransferEvent(ctx, event.ID, true, nil); notification != nil {
					notifications = append(notifications, notification)
				}
			}
		case schematype.TransferStatusSenderKeyTweaked:
			if senderOk && senderPubkey == subscriptionPubkey {
				if notification := s.buildTransferEvent(ctx, event.ID, true, nil); notification != nil {
					notifications = append(notifications, notification)
				}
			}
			if receiverOk && receiverPubkey == subscriptionPubkey {
				if notification := s.buildTransferEvent(ctx, event.ID, false, &identityPublicKey); notification != nil {
					notifications = append(notifications, notification)
				}
			}
		default:
			return []*pb.SubscribeToEventsResponse{}
		}
		return notifications
	}
	return []*pb.SubscribeToEventsResponse{}
}

func (s *EventRouter) buildTransferEvent(ctx context.Context, transferID uuid.UUID, isSender bool, receiverPubkey *keys.Public) *pb.SubscribeToEventsResponse {
	transferEnt, err := s.dbClient.Transfer.Query().
		Where(transfer.ID(transferID)).
		WithTransferReceivers().
		Only(ctx)
	if err != nil {
		s.logger.With(zap.Error(err)).Sugar().Warnf("failed to query transfer %s for stream event", transferID)
		return nil
	}

	var transferProto *pb.Transfer
	if receiverPubkey != nil && transferEnt.HasReceiver(*receiverPubkey) {
		transferProto, err = transferEnt.MarshalProtoForReceiver(ctx, *receiverPubkey)
	} else {
		transferProto, err = transferEnt.MarshalProto(ctx)
	}
	if err != nil {
		s.logger.With(zap.Error(err)).Sugar().Warnf("failed to marshal transfer %s for stream event", transferID)
		return nil
	}

	if isSender {
		return &pb.SubscribeToEventsResponse{
			Event: &pb.SubscribeToEventsResponse_SenderTransfer{
				SenderTransfer: &pb.TransferEvent{Transfer: transferProto},
			},
		}
	}
	return &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_ReceiverTransfer{
			ReceiverTransfer: &pb.TransferEvent{Transfer: transferProto},
		},
	}
}

func (s *EventRouter) processTokenTransactionNotification(ctx context.Context, event processEventPayload) *pb.SubscribeToEventsResponse {
	// The fan-out hook pre-filters by owner_public_key, so we know the
	// subscriber is involved. Query the transaction to build the response.
	tx, err := s.dbClient.TokenTransaction.Query().
		Where(tokentransaction.ID(event.ID)).
		WithSpentOutput().
		WithCreatedOutput().
		WithSparkInvoice().
		Only(ctx)
	if err != nil {
		s.logger.With(zap.Error(err)).Sugar().Warnf("failed to query token transaction %s for stream event", event.ID)
		return nil
	}

	tokenIDSet := make(map[string][]byte)
	for _, output := range tx.Edges.SpentOutput {
		tokenIDSet[string(output.TokenIdentifier)] = output.TokenIdentifier
	}
	for _, output := range tx.Edges.CreatedOutput {
		tokenIDSet[string(output.TokenIdentifier)] = output.TokenIdentifier
	}

	tokenIdentifiers := make([][]byte, 0, len(tokenIDSet))
	for _, id := range tokenIDSet {
		tokenIdentifiers = append(tokenIdentifiers, id)
	}

	sparkInvoices := make([]string, 0, len(tx.Edges.SparkInvoice))
	for _, inv := range tx.Edges.SparkInvoice {
		sparkInvoices = append(sparkInvoices, inv.SparkInvoice)
	}

	return &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_TokenTransaction{
			TokenTransaction: &pb.TokenTransactionEvent{
				TokenTransactionHash: tx.FinalizedTokenTransactionHash,
				TokenIdentifiers:     tokenIdentifiers,
				SparkInvoices:        sparkInvoices,
			},
		},
	}
}
