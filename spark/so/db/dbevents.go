package db

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
	"github.com/lightsparkdev/spark/so"
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

type channelChange struct {
	channel   string
	operation string
}

type EventData struct {
	Channel string
	Payload string
}

type DBEvents struct {
	ctx context.Context

	connector *so.DBConnector
	conn      *pgx.Conn

	waitForNotificationCancel context.CancelFunc
	mu                        sync.RWMutex

	listeners      map[string]map[listenerKey][]chan EventData
	channelChanges []channelChange

	logger  *zap.Logger
	metrics DBEventMetrics
}

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

func NewDBEvents(ctx context.Context, connector *so.DBConnector, logger *zap.Logger) (*DBEvents, error) {
	conn, err := connector.Pool().Acquire(ctx)
	if err != nil {
		return nil, err
	}

	rawConn := conn.Hijack()

	events := &DBEvents{
		ctx:            ctx,
		connector:      connector,
		listeners:      make(map[string]map[listenerKey][]chan EventData),
		conn:           rawConn,
		channelChanges: []channelChange{},
		logger:         logger,
		metrics:        NewDBEventMetrics(),
	}

	return events, nil
}

func (e *DBEvents) Start() error {
	return e.listenForEvents()
}

func (e *DBEvents) listenForEvents() error {
	for {
		err := e.waitForNotification()
		if err != nil {
			e.logger.With(zap.Error(err)).Error("error waiting for notification")

			if e.ctx.Err() != nil {
				return e.ctx.Err()
			}

			if e.conn.PgConn().IsClosed() {
				if err := e.reconnect(); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, puddle.ErrClosedPool) {
						return err
					}
					e.logger.With(zap.Error(err)).Error("error reconnecting")
				}
			}
		}
	}
}

func (e *DBEvents) waitForNotification() error {
	e.processChannelChanges()

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()

	e.mu.Lock()
	e.waitForNotificationCancel = cancel
	e.mu.Unlock()

	notification, err := e.conn.WaitForNotification(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	e.processNotification(notification)

	return nil
}

type Subscription struct {
	EventName string
	Field     string
	Value     string
}

func (e *DBEvents) AddListeners(subscriptions []Subscription) (chan EventData, func()) {
	e.mu.Lock()
	defer e.mu.Unlock()

	channel := make(chan EventData, 16)

	for _, subscription := range subscriptions {
		if _, exists := e.listeners[subscription.EventName]; !exists {
			e.listeners[subscription.EventName] = make(map[listenerKey][]chan EventData)
			e.channelChanges = append(e.channelChanges, channelChange{
				channel:   subscription.EventName,
				operation: "listen",
			})
		}

		listenerKey := listenerKey{
			Field: subscription.Field,
			Value: subscription.Value,
		}

		if existingChannels, exists := e.listeners[subscription.EventName][listenerKey]; exists {
			e.listeners[subscription.EventName][listenerKey] = append(existingChannels, channel)
		} else {
			e.listeners[subscription.EventName][listenerKey] = []chan EventData{channel}
		}
	}

	cleanup := func() {
		e.mu.Lock()
		defer e.mu.Unlock()

		for _, subscription := range subscriptions {
			if channels, exists := e.listeners[subscription.EventName]; exists {
				listenerKey := listenerKey{
					Field: subscription.Field,
					Value: subscription.Value,
				}

				if channelSlice, exists := channels[listenerKey]; exists {
					for i, ch := range channelSlice {
						if ch == channel {
							channelSlice = append(channelSlice[:i], channelSlice[i+1:]...)

							if len(channelSlice) == 0 {
								delete(channels, listenerKey)

								if len(channels) == 0 {
									delete(e.listeners, subscription.EventName)
									e.channelChanges = append(e.channelChanges, channelChange{
										channel:   subscription.EventName,
										operation: "unlisten",
									})
								}
							} else {
								channels[listenerKey] = channelSlice
							}
							break
						}
					}
				}
			}
		}

		close(channel)

		if len(e.channelChanges) > 0 && e.waitForNotificationCancel != nil {
			e.waitForNotificationCancel()
		}
	}

	if len(e.channelChanges) > 0 && e.waitForNotificationCancel != nil {
		e.waitForNotificationCancel()
	}

	return channel, cleanup
}

func (e *DBEvents) processNotification(notification *pgconn.Notification) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.metrics.listenCount.Add(e.ctx, 1, metric.WithAttributes(attribute.String("channel", notification.Channel)))

	if c, exists := e.listeners[notification.Channel]; exists {
		var payload map[string]any
		if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
			return
		}

		for field, value := range payload {
			listenerKey := listenerKey{Field: field, Value: value}
			if listeners, exists := c[listenerKey]; exists {
				eventData := EventData{
					Channel: notification.Channel,
					Payload: notification.Payload,
				}
				for _, channel := range listeners {
					select {
					case channel <- eventData:
						e.metrics.forwardCount.Add(
							e.ctx,
							1,
							metric.WithAttributes(
								attribute.String("channel", notification.Channel),
								attribute.String("result", "success"),
							),
						)
					default:
						e.logger.Sugar().Warnf("Listener channel is full (field: %s, value: %s)", field, value)
						e.metrics.forwardCount.Add(
							e.ctx,
							1,
							metric.WithAttributes(
								attribute.String("channel", notification.Channel),
								attribute.String("result", "failure"),
							),
						)
					}
				}
			}
		}
	} else {
		e.channelChanges = append(e.channelChanges, channelChange{
			channel:   notification.Channel,
			operation: "unlisten",
		})
		if e.waitForNotificationCancel != nil {
			e.waitForNotificationCancel()
		}
	}
}

func (e *DBEvents) processChannelChanges() {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, channelChange := range e.channelChanges {
		switch channelChange.operation {
		case "listen":
			err := e.startListening(channelChange.channel)
			if err != nil {
				e.logger.With(zap.Error(err)).Sugar().Errorf("error listening for channel %s", channelChange.channel)
			}
		case "unlisten":
			err := e.stopListening(channelChange.channel)
			if err != nil {
				e.logger.With(zap.Error(err)).Sugar().Errorf("error unlistening for channel %s", channelChange.channel)
			}
			delete(e.listeners, channelChange.channel)
		default:
			e.logger.Sugar().Errorf("invalid channel change operation %s", channelChange.operation)
		}
	}
	e.channelChanges = []channelChange{}
}

func (e *DBEvents) startListening(channel string) error {
	_, err := e.conn.Exec(e.ctx, "LISTEN "+channel)
	return err
}

func (e *DBEvents) stopListening(channel string) error {
	_, err := e.conn.Exec(e.ctx, "UNLISTEN "+channel)
	return err
}

func (e *DBEvents) reconnect() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	backoff := 100 * time.Millisecond
	const maxBackoff = time.Minute

	for {
		if e.conn != nil {
			if err := e.conn.Close(e.ctx); err != nil {
				e.logger.With(zap.Error(err)).Error("error closing connection")
				return err
			}
		}

		conn, err := e.connector.Pool().Acquire(e.ctx)
		if err == nil {
			e.conn = conn.Hijack()
			break
		} else if errors.Is(err, context.Canceled) || errors.Is(err, puddle.ErrClosedPool) {
			return err
		}

		e.logger.With(zap.Error(err)).Error("reconnect failed, retrying")

		select {
		case <-e.ctx.Done():
			return e.ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	return e.reestablishListeners()
}

func (e *DBEvents) reestablishListeners() error {
	for eventName := range e.listeners {
		if err := e.startListening(eventName); err != nil {
			return err
		}
	}

	return nil
}
