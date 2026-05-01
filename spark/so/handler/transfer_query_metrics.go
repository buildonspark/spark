package handler

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var transferQueryMeter = otel.Meter("handler.transfers")

var transferQueryDuration metric.Float64Histogram
var transferQueryResultCount metric.Float64Histogram

// queryPendingNilParticipantFallback counts QueryPendingTransfers calls that
// fell through to the legacy queryTransfers path because filter.Participant
// was nil under KnobReadMIMODataModelQueryPendingTransfers.
var queryPendingNilParticipantFallback metric.Int64Counter

func init() {
	var err error

	transferQueryDuration, err = transferQueryMeter.Float64Histogram(
		"spark_transfer_query_duration",
		metric.WithDescription("Duration of MIMO-gated transfer query paths"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000),
	)
	if err != nil {
		otel.Handle(err)
		if transferQueryDuration == nil {
			transferQueryDuration = noop.Float64Histogram{}
		}
	}

	transferQueryResultCount, err = transferQueryMeter.Float64Histogram(
		"spark_transfer_query_result_count",
		metric.WithDescription("Result count for MIMO-gated transfer query paths"),
		metric.WithUnit("{count}"),
		metric.WithExplicitBucketBoundaries(0, 1, 5, 10, 25, 50, 100, 250, 500, 1000, 5000, 50000),
	)
	if err != nil {
		otel.Handle(err)
		if transferQueryResultCount == nil {
			transferQueryResultCount = noop.Float64Histogram{}
		}
	}

	queryPendingNilParticipantFallback, err = transferQueryMeter.Int64Counter(
		"spark_query_pending_transfers_nil_participant_fallback",
		metric.WithDescription("QueryPendingTransfers calls that fell through to legacy because Participant was nil under the MIMO knob"),
	)
	if err != nil {
		otel.Handle(err)
		if queryPendingNilParticipantFallback == nil {
			queryPendingNilParticipantFallback = noop.Int64Counter{}
		}
	}
}

type transferQueryRecorder struct {
	startTime   time.Time
	queryPath   string
	mimoEnabled bool
	filterType  string
}

func newTransferQueryRecorder(queryPath string, mimoEnabled bool, filterType string) *transferQueryRecorder {
	return &transferQueryRecorder{
		startTime:   time.Now(),
		queryPath:   queryPath,
		mimoEnabled: mimoEnabled,
		filterType:  filterType,
	}
}

func (r *transferQueryRecorder) record(ctx context.Context, resultCount int, err error) {
	duration := time.Since(r.startTime).Seconds() * 1000

	attrs := []attribute.KeyValue{
		attribute.String("query_path", r.queryPath),
		attribute.Bool("mimo_enabled", r.mimoEnabled),
		attribute.String("filter_type", r.filterType),
		attribute.Bool("success", err == nil),
	}
	opts := metric.WithAttributes(attrs...)

	transferQueryResultCount.Record(ctx, float64(resultCount), opts)
	transferQueryDuration.Record(ctx, duration, opts)
}
