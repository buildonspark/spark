package handler

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type SendGossipHandler struct {
	config *so.Config
}

func NewSendGossipHandler(config *so.Config) *SendGossipHandler {
	return &SendGossipHandler{config: config}
}

func (h *SendGossipHandler) postSendingGossipMessage(
	ctx context.Context,
	message *pbgossip.GossipMessage,
	gossip *ent.Gossip,
	bitMap *common.BitMap,
) (*ent.Gossip, error) {
	logger := logging.GetLoggerFromContext(ctx)
	newStatus := st.GossipStatusPending
	if bitMap.IsAllSet() {
		newStatus = st.GossipStatusDelivered
	}
	gossip, err := gossip.Update().SetStatus(newStatus).SetReceipts(bitMap.Bytes()).Save(ctx)
	if err != nil {
		return nil, err
	}

	if bitMap.IsAllSet() {
		handler := NewGossipHandler(h.config)
		err = handler.HandleGossipMessage(ctx, message, true)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Handling for gossip message ID %s after full delivery failed with error: %v", message.MessageId, err)
			if status.Code(err) == codes.AlreadyExists {
				logger.Sugar().Infof("Gossip message %s already processed (AlreadyExists), treating as success", message.MessageId)
				return gossip, nil
			}
			if status.Code(err) == codes.Unavailable ||
				status.Code(err) == codes.Canceled ||
				strings.Contains(err.Error(), "context canceled") ||
				strings.Contains(err.Error(), "unexpected HTTP status code") {
				return nil, err
			}
			if isRetriableSQLStateError(err) {
				logger.Warn("retriable SQL error in gossip handling, should investigate root cause",
					zap.String("message_id", message.MessageId),
					zap.Error(err))
				return nil, err
			}
			logger.Sugar().Warnf("Non-retriable error for gossip message %s, not retrying: %v", message.MessageId, err)
		}
	}
	return gossip, nil
}

func (h *SendGossipHandler) sendGossipMessageToParticipant(ctx context.Context, gossip *pbgossip.GossipMessage, participant string) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Sending gossip message to participant %s", participant)
	operator, ok := h.config.SigningOperatorMap[participant]
	if !ok {
		return fmt.Errorf("operator %s not found", participant)
	}
	conn, err := operator.NewOperatorGRPCConnection()
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pbgossip.NewGossipServiceClient(conn)
	_, err = client.Gossip(ctx, gossip)
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			logger.Sugar().Infof("Gossip message already processed by participant %s (AlreadyExists), treating as success", participant)
			return nil
		}
		if status.Code(err) == codes.Unavailable ||
			status.Code(err) == codes.Canceled ||
			strings.Contains(err.Error(), "context canceled") ||
			strings.Contains(err.Error(), "unexpected HTTP status code") {
			return err
		}
		if isRetriableSQLStateError(err) {
			logger.Warn("retriable SQL error sending gossip to participant, should investigate root cause",
				zap.String("participant", participant),
				zap.Error(err))
			return err
		}
		logger.With(zap.Error(err)).Sugar().Errorf("Non-retriable error sending gossip to participant %s, not retrying", participant)
		return nil
	}

	logger.Sugar().Infof("Gossip message sent to participant: %s", participant)
	return nil
}

func (h *SendGossipHandler) CreateAndSendGossipMessage(ctx context.Context, gossipMsg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	messageBytes, err := proto.Marshal(gossipMsg)
	if err != nil {
		return nil, err
	}
	receipts := common.NewBitMap(len(participants)).Bytes()
	gossip, err := db.Gossip.Create().SetMessage(messageBytes).SetParticipants(participants).SetReceipts(receipts).Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("gossip message already exists: %w", err))
		}
		return nil, fmt.Errorf("failed to create gossip message: %w", err)
	}
	gossip, err = h.SendGossipMessage(ctx, gossip)
	if err != nil {
		return nil, err
	}
	return gossip, nil
}

func (h *SendGossipHandler) CreateCommitAndSendGossipMessage(ctx context.Context, gossipMsg *pbgossip.GossipMessage, participants []string) (*ent.Gossip, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	messageBytes, err := proto.Marshal(gossipMsg)
	if err != nil {
		return nil, err
	}
	receipts := common.NewBitMap(len(participants)).Bytes()
	gossip, err := db.Gossip.Create().SetMessage(messageBytes).SetParticipants(participants).SetReceipts(receipts).Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("gossip message already exists: %w", err))
		}
		return nil, fmt.Errorf("failed to create gossip message: %w", err)
	}
	err = ent.DbCommit(ctx)
	if err != nil {
		return nil, err
	}
	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	gossip, err = db.Gossip.Get(ctx, gossip.ID)
	if err != nil {
		return nil, err
	}
	gossip, err = h.SendGossipMessage(ctx, gossip)
	if err != nil {
		return nil, err
	}
	return gossip, nil
}

func (h *SendGossipHandler) SendGossipMessage(ctx context.Context, gossip *ent.Gossip) (*ent.Gossip, error) {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Sending gossip message %s", gossip.ID.String())
	bitMap := common.NewBitMapFromBytes(*gossip.Receipts, len(gossip.Participants))

	message := &pbgossip.GossipMessage{}
	if err := proto.Unmarshal(gossip.Message, message); err != nil {
		return nil, err
	}
	message.MessageId = gossip.ID.String()

	wg := sync.WaitGroup{}
	success := make(chan int, len(gossip.Participants))
	for i, participant := range gossip.Participants {
		if bitMap.Get(i) {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := h.sendGossipMessageToParticipant(ctx, message, participant)
			if err != nil {
				logger.Error("Failed to send gossip message", zap.Error(err))
			} else {
				success <- i
			}
		}(i)
	}
	wg.Wait()
	close(success)

	for i := range success {
		bitMap.Set(i, true)
	}
	gossip, err := h.postSendingGossipMessage(ctx, message, gossip, bitMap)
	if err != nil {
		return nil, err
	}
	return gossip, nil
}

// isRetriableSQLStateError returns true for transient database errors that might
// succeed on retry (connection issues, timeouts, resource exhaustion).
// Returns false for constraint violations which will always fail on retry.
func isRetriableSQLStateError(err error) bool {
	if err == nil {
		return false
	}
	// Constraint violations are never retriable
	if sqlgraph.IsConstraintError(err) {
		return false
	}
	errStr := err.Error()
	// Only consider SQLSTATE errors as potentially retriable
	if !strings.Contains(errStr, "SQLSTATE") {
		return false
	}
	// Class 23 errors (integrity constraint violations) are not retriable
	if strings.Contains(errStr, "SQLSTATE 23") {
		return false
	}
	return true
}
