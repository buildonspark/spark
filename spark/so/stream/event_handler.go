package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/treenode"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

const (
	eventNameDepositAddress = "depositaddress"
	eventNameTransfer       = "transfer"
)

type eventListener struct {
	channel chan db.EventData
	cleanup func()
}

type EventRouter struct {
	streams   sync.Map
	mutexes   sync.Map
	listeners map[keys.Public]eventListener
	mu        sync.RWMutex
	dbEvents  *db.DBEvents
	logger    *slog.Logger
	dbClient  *ent.Client
}

func NewEventRouter(dbClient *ent.Client, dbEvents *db.DBEvents, logger *slog.Logger) *EventRouter {
	defaultRouter := &EventRouter{
		streams:   sync.Map{},
		mutexes:   sync.Map{},
		listeners: make(map[keys.Public]eventListener),
		mu:        sync.RWMutex{},
		dbEvents:  dbEvents,
		logger:    logger,
		dbClient:  dbClient,
	}

	return defaultRouter
}

func (s *EventRouter) SubscribeToEvents(identityPublicKey keys.Public, stream pb.SparkService_SubscribeToEventsServer) error {
	notificationChan := s.RegisterStream(identityPublicKey, stream)

	connectedEvent := &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_Connected{
			Connected: &pb.ConnectedEvent{},
		},
	}

	if err := stream.Send(connectedEvent); err != nil {
		s.removeStream(identityPublicKey, stream)
		return nil
	}

	for {
		select {
		case <-stream.Context().Done():
			s.removeStream(identityPublicKey, stream)
			return nil
		case eventData, ok := <-notificationChan:
			if !ok {
				s.removeStream(identityPublicKey, stream)
				return nil
			}
			if err := s.processNotification(eventData, identityPublicKey); err != nil {
				s.logger.Error("Failed to process notification", "error", err)
			}
		}
	}
}

func (s *EventRouter) RegisterStream(identityPublicKey keys.Public, stream pb.SparkService_SubscribeToEventsServer) chan db.EventData {
	mutex, _ := s.mutexes.LoadOrStore(identityPublicKey, &sync.Mutex{})
	mutex.(*sync.Mutex).Lock()
	defer mutex.(*sync.Mutex).Unlock()

	existingStreams, _ := s.streams.LoadOrStore(identityPublicKey, []pb.SparkService_SubscribeToEventsServer{})
	streams := existingStreams.([]pb.SparkService_SubscribeToEventsServer)

	streams = append(streams, stream)
	s.streams.Store(identityPublicKey, streams)

	notificationChan := s.createNotificationChannel(identityPublicKey)

	return notificationChan
}

func (s *EventRouter) createNotificationChannel(identityPublicKey keys.Public) chan db.EventData {
	s.mu.Lock()
	defer s.mu.Unlock()

	if channel, ok := s.listeners[identityPublicKey]; ok {
		return channel.channel
	}

	notificationChan, cleanup := s.dbEvents.AddListeners([]db.Subscription{
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
	})

	s.listeners[identityPublicKey] = eventListener{
		channel: notificationChan,
		cleanup: cleanup,
	}

	return notificationChan
}

type processEventPayload struct {
	ID     uuid.UUID
	Fields map[string]any
}

func (s *EventRouter) processNotification(eventData db.EventData, identityPublicKey keys.Public) error {
	if _, exists := s.streams.Load(identityPublicKey); exists {
		var eventJson map[string]any
		err := json.Unmarshal([]byte(eventData.Payload), &eventJson)
		if err != nil {
			s.logger.Error("Failed to unmarshal event data", "error", err)
			return err
		}

		idStr := eventJson["id"].(string)
		id, err := uuid.Parse(idStr)
		if err != nil {
			s.logger.Error("Failed to parse ID as UUID", "error", err)
			return err
		}

		delete(eventJson, "id")

		event := processEventPayload{
			ID:     id,
			Fields: eventJson,
		}

		var notification *pb.SubscribeToEventsResponse
		switch eventData.Channel {
		case eventNameDepositAddress:
			notification = s.processDepositNotification(event, identityPublicKey)
		case eventNameTransfer:
			notification = s.processTransferNotification(event, identityPublicKey)
		default:
			return fmt.Errorf("unknown event type: %s", eventData.Channel)
		}

		if notification != nil {
			if err := s.notifyUser(identityPublicKey, notification); err != nil {
				return fmt.Errorf("failed to notify user: %w", err)
			}
		}
	}

	return nil
}

func (s *EventRouter) processDepositNotification(event processEventPayload, identityPublicKey keys.Public) *pb.SubscribeToEventsResponse {
	if _, exists := event.Fields["confirmation_txid"]; exists {
		depositaddress, err := s.dbClient.DepositAddress.Query().Where(depositaddress.ID(event.ID)).Only(context.Background())
		if err != nil {
			return nil
		}

		treeNode, err := s.dbClient.TreeNode.Query().Where(treenode.ID(depositaddress.NodeID)).Only(context.Background())
		if err != nil {
			// TODO: Fine to silently ignore this
			// If tree node doesn't exist maybe we can inform client that they can claim the deposit?
			return nil
		} else {
			treeNodeProto, err := treeNode.MarshalSparkProto(context.Background())
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
	}
	return nil
}

func (s *EventRouter) processTransferNotification(event processEventPayload, identityPublicKey keys.Public) *pb.SubscribeToEventsResponse {
	if statusStr, exists := event.Fields["status"]; exists {
		status := schematype.TransferStatus(statusStr.(string))

		if status == schematype.TransferStatusSenderKeyTweaked {
			transfer, err := s.dbClient.Transfer.Query().Where(transfer.ID(event.ID)).Only(context.Background())
			if err != nil {
				return nil
			}

			transferProto, err := transfer.MarshalProto(context.Background())
			if err != nil {
				return nil
			}

			return &pb.SubscribeToEventsResponse{
				Event: &pb.SubscribeToEventsResponse_Transfer{
					Transfer: &pb.TransferEvent{
						Transfer: transferProto,
					},
				},
			}
		}
	}
	return nil
}

func (s *EventRouter) removeStream(identityPublicKey keys.Public, stream pb.SparkService_SubscribeToEventsServer) {
	mutex, exists := s.mutexes.Load(identityPublicKey)
	if !exists {
		return
	}
	mutex.(*sync.Mutex).Lock()
	defer mutex.(*sync.Mutex).Unlock()

	// Remove this specific stream
	if existingStreams, exists := s.streams.Load(identityPublicKey); exists {
		streams := existingStreams.([]pb.SparkService_SubscribeToEventsServer)

		var newStreams []pb.SparkService_SubscribeToEventsServer
		for _, s := range streams {
			if s != stream {
				newStreams = append(newStreams, s)
			}
		}

		if len(newStreams) == 0 {
			s.streams.Delete(identityPublicKey)
			s.mutexes.Delete(identityPublicKey)

			if eventListener, exists := s.listeners[identityPublicKey]; exists {
				eventListener.cleanup()
			}

			delete(s.listeners, identityPublicKey)
		} else {
			s.streams.Store(identityPublicKey, newStreams)
		}
	}
}

func (s *EventRouter) notifyUser(identityPublicKey keys.Public, message *pb.SubscribeToEventsResponse) error {
	if streams, exists := s.streams.Load(identityPublicKey); exists {
		streamList := streams.([]pb.SparkService_SubscribeToEventsServer)

		for _, stream := range streamList {
			if stream.Context().Err() == nil {
				if err := stream.Send(message); err != nil {
					s.removeStream(identityPublicKey, stream)
				}
			} else {
				s.removeStream(identityPublicKey, stream)
			}
		}
	}
	return nil
}
