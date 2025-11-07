package db

import (
	"context"
	"sync"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var (
	// Metrics
	txDurationHistogram metric.Float64Histogram
	txCounter           metric.Int64Counter
	txActiveGauge       metric.Int64UpDownCounter

	// Common attribute values
	attrOperationCommit   = attribute.String("operation", "commit")
	attrOperationRollback = attribute.String("operation", "rollback")
	attrOperationBegin    = attribute.String("operation", "begin")
	attrStatusSuccess     = attribute.String("status", "success")
	attrStatusError       = attribute.String("status", "error")

	// Initialize metrics
	_ = initMetrics()
)

func initMetrics() error {
	meter := otel.GetMeterProvider().Meter("spark.db")

	var err error
	txDurationHistogram, err = meter.Float64Histogram(
		"db_transaction_duration",
		metric.WithDescription("Database transaction duration in milliseconds"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(
			0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1,
			5, 10, 25, 50, 100, 250, 500,
			1000, 2500, 5000, 10000, 25000, 50000, 100000,
		),
	)
	if err != nil {
		return err
	}

	txCounter, err = meter.Int64Counter(
		"db_transactions_total",
		metric.WithDescription("Total number of database transactions"),
	)
	if err != nil {
		return err
	}

	txActiveGauge, err = meter.Int64UpDownCounter(
		"db_transactions_active",
		metric.WithDescription("Number of currently active database transactions"),
	)
	if err != nil {
		return err
	}

	return nil
}

// addTraceEvent adds a trace event if a span is available
func addTraceEvent(ctx context.Context, operation string, duration float64, err error) {
	span := trace.SpanFromContext(ctx)
	if span != nil {
		eventName := "db.transaction." + operation
		span.AddEvent(eventName, trace.WithAttributes(
			getTraceAttributes(operation, duration, err)...,
		))
	}
}

// getTraceAttributes returns the attributes for trace events
// operation: the operation type (begin, commit, rollback)
// duration: duration in seconds (0 for operations without duration)
// err: error if the operation failed - optional (status is inferred from this)
func getTraceAttributes(operation string, duration float64, err error) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("db.transaction.operation", operation),
	}
	if duration > 0 {
		attrs = append(attrs, attribute.Float64("db.transaction.duration_seconds", duration))
	}

	if err != nil {
		attrs = append(attrs, attrStatusError, attribute.String("error", err.Error()))
	} else {
		attrs = append(attrs, attrStatusSuccess)
	}

	return attrs
}

// MetricsTxProvider wraps another TxProvider and adds metrics tracking for database transactions.
// It records transaction duration, counts, and active transaction gauges, as well as trace events.
type MetricsTxProvider struct {
	wrapped     ent.TxProvider
	metricAttrs []attribute.KeyValue
	mu          sync.Mutex
	startTime   time.Time
}

// NewMetricsTxProvider creates a new MetricsTxProvider that wraps the given TxProvider.
func NewMetricsTxProvider(provider ent.TxProvider, metricAttrs []attribute.KeyValue) *MetricsTxProvider {
	return &MetricsTxProvider{
		wrapped:     provider,
		metricAttrs: metricAttrs,
	}
}

func (m *MetricsTxProvider) GetOrBeginTx(ctx context.Context) (*ent.Tx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	logger := logging.GetLoggerFromContext(ctx)

	// Record transaction begin
	txActiveGauge.Add(ctx, 1, metric.WithAttributes(m.getGaugeAttributes(attrOperationBegin)...))

	tx, err := m.wrapped.GetOrBeginTx(ctx)
	if err != nil {
		logger.Error("Failed to create new transaction", zap.Error(err))
		// Decrement on error
		txActiveGauge.Add(ctx, -1, metric.WithAttributes(m.getGaugeAttributes(attrOperationBegin)...))
		addTraceEvent(ctx, "begin", 0, err)
		return nil, err
	}

	m.startTime = time.Now()
	addTraceEvent(ctx, "begin", 0, nil)

	// Wrap commit hook
	tx.OnCommit(func(fn ent.Committer) ent.Committer {
		return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
			m.mu.Lock()
			duration := time.Since(m.startTime).Seconds()
			durationMs := duration * 1000
			m.mu.Unlock()

			err := fn.Commit(ctx, tx)
			var attrs []attribute.KeyValue
			if err != nil {
				logger.Error("Failed to commit transaction", zap.Error(err))
				attrs = m.getOperationAttributes(attrOperationCommit, attrStatusError)
				addTraceEvent(ctx, "commit", duration, err)
			} else {
				attrs = m.getOperationAttributes(attrOperationCommit, attrStatusSuccess)
				txDurationHistogram.Record(ctx, durationMs, metric.WithAttributes(attrs...))
				txActiveGauge.Add(ctx, -1, metric.WithAttributes(m.getGaugeAttributes(attrOperationCommit)...))
				addTraceEvent(ctx, "commit", duration, nil)
			}

			txCounter.Add(ctx, 1, metric.WithAttributes(attrs...))

			return err
		})
	})

	// Wrap rollback hook
	tx.OnRollback(func(fn ent.Rollbacker) ent.Rollbacker {
		return ent.RollbackFunc(func(ctx context.Context, tx *ent.Tx) error {
			m.mu.Lock()
			duration := time.Since(m.startTime).Seconds()
			durationMs := duration * 1000
			m.mu.Unlock()

			err := fn.Rollback(ctx, tx)
			var attrs []attribute.KeyValue
			if err != nil {
				logger.Error("Failed to rollback transaction", zap.Error(err))
				attrs = m.getOperationAttributes(attrOperationRollback, attrStatusError)
				addTraceEvent(ctx, "rollback", duration, err)
			} else {
				attrs = m.getOperationAttributes(attrOperationRollback, attrStatusSuccess)
				addTraceEvent(ctx, "rollback", duration, nil)
			}

			txDurationHistogram.Record(ctx, durationMs, metric.WithAttributes(attrs...))
			txCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
			txActiveGauge.Add(ctx, -1, metric.WithAttributes(m.getGaugeAttributes(attrOperationRollback)...))

			return err
		})
	})

	return tx, nil
}

func (m *MetricsTxProvider) GetClient(ctx context.Context) (*ent.Client, error) {
	return m.wrapped.GetClient(ctx)
}

// getGaugeAttributes returns the attributes for gauge operations
func (m *MetricsTxProvider) getGaugeAttributes(operationAttr attribute.KeyValue) []attribute.KeyValue {
	return append([]attribute.KeyValue{operationAttr}, m.metricAttrs...)
}

// getOperationAttributes returns the attributes for a specific operation
func (m *MetricsTxProvider) getOperationAttributes(operationAttr attribute.KeyValue, statusAttr attribute.KeyValue) []attribute.KeyValue {
	attrs := []attribute.KeyValue{operationAttr, statusAttr}
	attrs = append(attrs, m.metricAttrs...)
	return attrs
}
