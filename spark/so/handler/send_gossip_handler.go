package handler

import (
	"context"
	"fmt"
	"sync"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
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
			return nil, err
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
	client := pbgossip.NewGossipServiceClient(conn)
	_, err = client.Gossip(ctx, gossip)
	if err != nil {
		if status.Code(err) == codes.Unavailable {
			return err
		}

		logger.With(zap.Error(err)).Sugar().Errorf("Gossip message sent to participant %s with error", participant)
		return nil
	}

	logger.Sugar().Infof("Gossip message sent to participant", participant)
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
