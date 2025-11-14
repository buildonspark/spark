package db

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/eventmessage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
)

type listenerKey struct {
	Field string
	Value any
}

type EventData struct {
	Channel string
	Payload string
}

type eventCursor struct {
	createTime time.Time
	id         uuid.UUID
	valid      bool
}

type DBEvents struct {
	ctx    context.Context
	client *ent.Client

	mu        sync.RWMutex
	listeners map[string]map[listenerKey][]chan EventData

	logger       *zap.Logger
	metrics      DBEventMetrics
	pollInterval time.Duration
	batchSize    int
	wakeup       chan struct{}

	lastCursor eventCursor
}

const (
	defaultPollInterval = 200 * time.Millisecond
	defaultBatchSize    = 512
)

type DBEventMetrics struct {
	listenCount  metric.Int64Counter
	forwardCount metric.Int64Counter
}

func NewDBEventMetrics() DBEventMetrics {
	meter := otel.Meter("spark.db")

	listenCount, err := meter.Int64Counter(
		"spark_dbevents_received_per_channel",
		metric.WithDescription("Number of events received per channel"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if listenCount == nil {
			listenCount = noop.Int64Counter{}
		}
	}

	forwardCount, err := meter.Int64Counter(
		"spark_dbevents_forwarded_per_channel",
		metric.WithDescription("Number of events forwarded to listeners per channel"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if forwardCount == nil {
			forwardCount = noop.Int64Counter{}
		}
	}

	return DBEventMetrics{
		listenCount:  listenCount,
		forwardCount: forwardCount,
	}
}

func NewDBEvents(ctx context.Context, client *ent.Client, logger *zap.Logger) (*DBEvents, error) {
	cursor, err := latestCursor(ctx, client)
	if err != nil {
		return nil, err
	}

	events := &DBEvents{
		ctx:          ctx,
		client:       client,
		listeners:    make(map[string]map[listenerKey][]chan EventData),
		logger:       logger,
		metrics:      NewDBEventMetrics(),
		pollInterval: defaultPollInterval,
		batchSize:    defaultBatchSize,
		wakeup:       make(chan struct{}, 1),
		lastCursor:   cursor,
	}

	events.signalWakeup()

	return events, nil
}

func (e *DBEvents) Start() error {
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return nil
		case <-ticker.C:
			e.pollOnce()
		case <-e.wakeup:
			e.pollOnce()
		}
	}
}

type Subscription struct {
	EventName string
	Field     string
	Value     string
}

func (e *DBEvents) AddListeners(subscriptions []Subscription) (chan EventData, func()) {
	e.mu.Lock()
	defer e.mu.Unlock()

	channel := make(chan EventData, 32)

	for _, subscription := range subscriptions {
		if _, exists := e.listeners[subscription.EventName]; !exists {
			e.listeners[subscription.EventName] = make(map[listenerKey][]chan EventData)
		}

		key := listenerKey{
			Field: subscription.Field,
			Value: subscription.Value,
		}

		e.listeners[subscription.EventName][key] = append(e.listeners[subscription.EventName][key], channel)
	}

	cleanup := func() {
		e.mu.Lock()
		defer e.mu.Unlock()

		for _, subscription := range subscriptions {
			e.removeListenerLocked(subscription, channel)
		}

		close(channel)
	}

	e.signalWakeup()

	return channel, cleanup
}

func (e *DBEvents) removeListenerLocked(subscription Subscription, channel chan EventData) {
	channels, exists := e.listeners[subscription.EventName]
	if !exists {
		return
	}

	key := listenerKey{
		Field: subscription.Field,
		Value: subscription.Value,
	}

	channelSlice, exists := channels[key]
	if !exists {
		return
	}

	for i, ch := range channelSlice {
		if ch == channel {
			channelSlice = append(channelSlice[:i], channelSlice[i+1:]...)
			break
		}
	}

	if len(channelSlice) == 0 {
		delete(channels, key)
	} else {
		channels[key] = channelSlice
	}

	if len(channels) == 0 {
		delete(e.listeners, subscription.EventName)
	}
}

func (e *DBEvents) pollOnce() {
	channels := e.activeChannels()
	if len(channels) == 0 {
		return
	}

	messages, err := e.fetchEvents(channels)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			e.logger.With(zap.Error(err)).Error("error polling event messages")
		}
		return
	}

	if len(messages) == 0 {
		return
	}

	e.handleMessages(messages)

	if len(messages) == e.batchSize {
		e.signalWakeup()
	}
}

func (e *DBEvents) activeChannels() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	channels := make([]string, 0, len(e.listeners))
	for name := range e.listeners {
		channels = append(channels, name)
	}
	return channels
}

func (e *DBEvents) fetchEvents(channels []string) ([]*ent.EventMessage, error) {
	query := e.client.EventMessage.
		Query().
		Where(eventmessage.ChannelIn(channels...)).
		Order(eventmessage.ByCreateTime(), eventmessage.ByID()).
		Limit(e.batchSize)

	if e.lastCursor.valid {
		query = query.Where(eventmessage.GreaterThanCursor(e.lastCursor.createTime, e.lastCursor.id))
	}

	return query.All(e.ctx)
}

func (e *DBEvents) handleMessages(messages []*ent.EventMessage) {
	for _, msg := range messages {
		e.processMessage(msg)
		e.lastCursor = eventCursor{
			createTime: msg.CreateTime,
			id:         msg.ID,
			valid:      true,
		}
	}
}

func (e *DBEvents) processMessage(msg *ent.EventMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.metrics.listenCount.Add(e.ctx, 1, metric.WithAttributes(attribute.String("channel", msg.Channel)))

	c, exists := e.listeners[msg.Channel]
	if !exists {
		return
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
		return
	}

	for field, value := range payload {
		key := listenerKey{Field: field, Value: value}
		if listeners, found := c[key]; found {
			eventData := EventData{
				Channel: msg.Channel,
				Payload: msg.Payload,
			}
			for _, ch := range listeners {
				select {
				case ch <- eventData:
					e.metrics.forwardCount.Add(
						e.ctx,
						1,
						metric.WithAttributes(
							attribute.String("channel", msg.Channel),
							attribute.String("result", "success"),
						),
					)
				default:
					e.logger.Sugar().Warnf("Listener channel is full (field: %s, value: %s)", field, value)
					e.metrics.forwardCount.Add(
						e.ctx,
						1,
						metric.WithAttributes(
							attribute.String("channel", msg.Channel),
							attribute.String("result", "failure"),
						),
					)
				}
			}
		}
	}
}

func (e *DBEvents) signalWakeup() {
	select {
	case e.wakeup <- struct{}{}:
	default:
	}
}

func latestCursor(ctx context.Context, client *ent.Client) (eventCursor, error) {
	msg, err := client.EventMessage.
		Query().
		Order(
			eventmessage.ByCreateTime(entsql.OrderDesc()),
			eventmessage.ByID(entsql.OrderDesc()),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return eventCursor{}, nil
		}
		return eventCursor{}, err
	}

	return eventCursor{
		createTime: msg.CreateTime,
		id:         msg.ID,
		valid:      true,
	}, nil
}
