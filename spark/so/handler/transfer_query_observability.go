package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
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

type transferQueryAttrs struct {
	QueryPath       string
	MIMOEnabled     bool
	FilterType      string
	HasPubkey       bool
	HasStatusFilter bool
	HasTypeFilter   bool
}

type transferQueryRecorder struct {
	startTime time.Time
	attrs     transferQueryAttrs
}

func newTransferQueryRecorder(attrs transferQueryAttrs) *transferQueryRecorder {
	return &transferQueryRecorder{
		startTime: time.Now(),
		attrs:     attrs,
	}
}

func (r *transferQueryRecorder) record(ctx context.Context, resultCount int, err error) {
	duration := time.Since(r.startTime).Seconds() * 1000

	attrs := []attribute.KeyValue{
		attribute.String("query_path", r.attrs.QueryPath),
		attribute.Bool("mimo_enabled", r.attrs.MIMOEnabled),
		attribute.String("filter_type", r.attrs.FilterType),
		attribute.Bool("has_pubkey", r.attrs.HasPubkey),
		attribute.Bool("has_status_filter", r.attrs.HasStatusFilter),
		attribute.Bool("has_type_filter", r.attrs.HasTypeFilter),
		attribute.Bool("success", err == nil),
	}
	opts := metric.WithAttributes(attrs...)

	transferQueryResultCount.Record(ctx, float64(resultCount), opts)
	transferQueryDuration.Record(ctx, duration, opts)
}

// shortPubkeyHash returns the first 8 bytes of sha256(pubkey) as a 16-char
// hex string. Used to pseudonymize identity pubkeys in logs while keeping
// per-wallet grouping intact. Returns "" for empty input.
func shortPubkeyHash(pubkey []byte) string {
	if len(pubkey) == 0 {
		return ""
	}
	sum := sha256.Sum256(pubkey)
	return hex.EncodeToString(sum[:8])
}

// logQueryTransfersInvocation emits a structured Info log of the caller's
// filter parameters. Gated by KnobLogTransferQueryInvocations (0–100 per-call
// sampling rate, default 0). The participant pubkey is hashed (sha256[:8]) so
// per-wallet grouping survives without writing raw identity pubkeys to logs.
func logQueryTransfersInvocation(ctx context.Context, queryPath string, filter *pb.TransferFilter, extra ...zap.Field) {
	if !knobs.GetKnobsService(ctx).RolloutRandom(knobs.KnobLogTransferQueryInvocations, 0) {
		return
	}
	fields := []zap.Field{zap.String("query_path", queryPath)}
	if filter != nil {
		participantType := "none"
		var participantPubkeyHash string
		switch p := filter.Participant.(type) {
		case *pb.TransferFilter_ReceiverIdentityPublicKey:
			participantType = "receiver"
			participantPubkeyHash = shortPubkeyHash(p.ReceiverIdentityPublicKey)
		case *pb.TransferFilter_SenderIdentityPublicKey:
			participantType = "sender"
			participantPubkeyHash = shortPubkeyHash(p.SenderIdentityPublicKey)
		case *pb.TransferFilter_SenderOrReceiverIdentityPublicKey:
			participantType = "sender_or_receiver"
			participantPubkeyHash = shortPubkeyHash(p.SenderOrReceiverIdentityPublicKey)
		}

		types := make([]string, 0, len(filter.GetTypes()))
		for _, t := range filter.GetTypes() {
			types = append(types, t.String())
		}
		statuses := make([]string, 0, len(filter.GetStatuses()))
		for _, s := range filter.GetStatuses() {
			statuses = append(statuses, s.String())
		}

		fields = append(fields,
			zap.String("network", filter.GetNetwork().String()),
			zap.String("participant_type", participantType),
			zap.String("participant_pubkey_hash", participantPubkeyHash),
			zap.Strings("types", types),
			zap.Strings("statuses", statuses),
			zap.Int("transfer_ids_count", len(filter.GetTransferIds())),
			zap.Int64("limit", filter.GetLimit()),
			zap.Int64("offset", filter.GetOffset()),
			zap.String("order", filter.GetOrder().String()),
			zap.Bool("has_created_after", filter.GetCreatedAfter() != nil),
			zap.Bool("has_created_before", filter.GetCreatedBefore() != nil),
		)
	}
	fields = append(fields, extra...)
	logging.GetLoggerFromContext(ctx).Info("transfer query invoked", fields...)
}
