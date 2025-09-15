package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"go.uber.org/zap"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

const (
	eventNameDepositAddress = "depositaddress"
	eventNameTransfer       = "transfer"
)

type EventRouter struct {
	dbEvents *db.DBEvents
	logger   *zap.Logger
	dbClient *ent.Client
}

func NewEventRouter(dbClient *ent.Client, dbEvents *db.DBEvents, logger *zap.Logger) *EventRouter {
	defaultRouter := &EventRouter{
		dbEvents: dbEvents,
		logger:   logger,
		dbClient: dbClient,
	}

	return defaultRouter
}

func (s *EventRouter) SubscribeToEvents(identityPublicKey keys.Public, stream pb.SparkService_SubscribeToEventsServer) error {
	notificationChan, cleanup := s.createNotificationChannel(identityPublicKey)
	defer cleanup()

	connectedEvent := &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_Connected{
			Connected: &pb.ConnectedEvent{},
		},
	}

	if err := stream.Send(connectedEvent); err != nil {
		return nil
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case eventData, ok := <-notificationChan:
			if !ok {
				return nil
			}

			notification, err := s.processNotification(stream.Context(), eventData, identityPublicKey)

			if err != nil {
				s.logger.With(zap.Error(err)).Error("Failed to process notification")
			} else if notification != nil {
				if err := stream.Send(notification); err != nil {
					return nil
				}
			}
		}
	}
}

func (s *EventRouter) createNotificationChannel(identityPublicKey keys.Public) (chan db.EventData, func()) {
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

	return notificationChan, cleanup
}

type processEventPayload struct {
	ID     uuid.UUID
	Fields map[string]any
}

func (s *EventRouter) processNotification(ctx context.Context, eventData db.EventData, identityPublicKey keys.Public) (*pb.SubscribeToEventsResponse, error) {
	var eventJson map[string]any
	err := json.Unmarshal([]byte(eventData.Payload), &eventJson)
	if err != nil {
		s.logger.With(zap.Error(err)).Error("Failed to unmarshal event data")
		return nil, err
	}

	idStr := eventJson["id"].(string)
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

	var notification *pb.SubscribeToEventsResponse
	switch eventData.Channel {
	case eventNameDepositAddress:
		notification = s.processDepositNotification(ctx, event, identityPublicKey)
	case eventNameTransfer:
		notification = s.processTransferNotification(ctx, event, identityPublicKey)
	default:
		return nil, fmt.Errorf("unknown event type: %s", eventData.Channel)
	}

	return notification, nil
}

func (s *EventRouter) processDepositNotification(ctx context.Context, event processEventPayload, identityPublicKey keys.Public) *pb.SubscribeToEventsResponse {
	if _, exists := event.Fields["confirmation_txid"]; exists {
		depositaddress, err := s.dbClient.DepositAddress.Query().Where(depositaddress.ID(event.ID)).Only(ctx)
		if err != nil {
			return nil
		}

		treeNode, err := s.dbClient.TreeNode.Query().Where(treenode.ID(depositaddress.NodeID)).Only(ctx)
		if err != nil {
			// TODO: Fine to silently ignore this
			// If tree node doesn't exist maybe we can inform client that they can claim the deposit?
			return nil
		} else {
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
	}
	return nil
}

func (s *EventRouter) processTransferNotification(ctx context.Context, event processEventPayload, identityPublicKey keys.Public) *pb.SubscribeToEventsResponse {
	if statusStr, exists := event.Fields["status"]; exists {
		status := schematype.TransferStatus(statusStr.(string))

		if status == schematype.TransferStatusSenderKeyTweaked {
			transfer, err := s.dbClient.Transfer.Query().Where(transfer.ID(event.ID)).Only(ctx)
			if err != nil {
				return nil
			}

			transferProto, err := transfer.MarshalProto(ctx)
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
