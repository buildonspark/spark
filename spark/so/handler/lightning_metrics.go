package handler

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	lightningFlowStorePreimageShare = "store_preimage_share"
	lightningFlowGetPreimageShare   = "get_preimage_share"
	lightningFlowInitiatePreimage   = "initiate_preimage_swap"
	lightningFlowProvidePreimage    = "provide_preimage"

	lightningFlowPathUnknown        = "unknown"
	lightningFlowPathSend           = "send"
	lightningFlowPathReceiveHodl    = "receive_hodl"
	lightningFlowPathReceiveNonHodl = "receive_non_hodl"

	lightningPhaseValidate         = "validate"
	lightningPhaseConsensusExecute = "consensus_execute"
	lightningPhaseCoordinatorStore = "coordinator_store"
	lightningPhaseBuildHTLCRefunds = "build_htlc_refunds"
	lightningPhaseCreateTransfer   = "create_transfer"
	lightningPhaseSignRefunds      = "sign_refunds"
	lightningPhaseApplySignatures  = "apply_signatures"
	lightningPhaseStoreSignedTxs   = "store_signed_txs"
	lightningPhaseStorePreimage    = "store_preimage"
	lightningPhaseFanout           = "fanout"
	lightningPhasePostFanoutCommit = "post_fanout_commit"
	lightningPhaseRecoverPreimage  = "recover_preimage"
	lightningPhaseSendGossip       = "send_gossip"
	lightningPhaseReloadTransfer   = "reload_transfer"
	lightningPhaseMarshalTransfer  = "marshal_transfer"

	lightningOperationStorePreimageShare = "store_preimage_share"
	lightningOperationGetPreimageShare   = "get_preimage_share"
	lightningOperationProvidePreimage    = "provide_preimage"

	lightningResultSuccess     = "success"
	lightningResultError       = "failure"
	lightningResultTimeout     = "timeout"
	lightningResultCanceled    = "canceled"
	lightningResultUnavailable = "unavailable"
)

var (
	lightningFlowKey                = attribute.Key("flow")
	lightningFlowPathKey            = attribute.Key("path")
	lightningPhaseKey               = attribute.Key("phase")
	lightningResultKey              = attribute.Key("result")
	lightningOperationKey           = attribute.Key("operation")
	lightningTargetOperatorIndexKey = attribute.Key("target_operator_index")
)

var lightningMetricBuckets = []float64{
	100, 500, 1000, 5000, 15000, 30000,
}

type lightningMetricInstruments struct {
	flowDuration       metric.Float64Histogram
	flowFailures       metric.Int64Counter
	phaseDuration      metric.Float64Histogram
	operatorRPC        metric.Float64Histogram
	operatorRPCFailure metric.Int64Counter
}

var getLightningMetricInstruments = sync.OnceValue(func() *lightningMetricInstruments {
	meter := otel.Meter("handler.lightning")

	flowDuration, err := meter.Float64Histogram(
		"spark_lightning_flow_duration_milliseconds",
		metric.WithDescription("Duration of Lightning flow operations"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(lightningMetricBuckets...),
	)
	if err != nil {
		otel.Handle(err)
		flowDuration = noop.Float64Histogram{}
	}

	flowFailures, err := meter.Int64Counter(
		"spark_lightning_flow_failures_total",
		metric.WithDescription("Total number of failed Lightning flow operations"),
		metric.WithUnit("1"),
	)
	if err != nil {
		otel.Handle(err)
		flowFailures = noop.Int64Counter{}
	}

	phaseDuration, err := meter.Float64Histogram(
		"spark_lightning_flow_phase_duration_milliseconds",
		metric.WithDescription("Duration of Lightning flow phases"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(lightningMetricBuckets...),
	)
	if err != nil {
		otel.Handle(err)
		phaseDuration = noop.Float64Histogram{}
	}

	operatorRPC, err := meter.Float64Histogram(
		"spark_operator_fanout_rpc_duration_milliseconds",
		metric.WithDescription("Duration of outbound operator fan-out RPCs used by Lightning flows"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(lightningMetricBuckets...),
	)
	if err != nil {
		otel.Handle(err)
		operatorRPC = noop.Float64Histogram{}
	}

	operatorRPCFailure, err := meter.Int64Counter(
		"operator_fanout_rpc_failures_total",
		metric.WithDescription("Total number of failed outbound operator fan-out RPCs used by Lightning flows"),
		metric.WithUnit("1"),
	)
	if err != nil {
		otel.Handle(err)
		operatorRPCFailure = noop.Int64Counter{}
	}

	return &lightningMetricInstruments{
		flowDuration:       flowDuration,
		flowFailures:       flowFailures,
		phaseDuration:      phaseDuration,
		operatorRPC:        operatorRPC,
		operatorRPCFailure: operatorRPCFailure,
	}
})

func observeLightningFlow(ctx context.Context, flow, path string, start time.Time, err error) {
	instruments := getLightningMetricInstruments()
	result := classifyLightningMetricResult(err)
	attrs := metric.WithAttributes(
		lightningFlowKey.String(flow),
		lightningFlowPathKey.String(path),
		lightningResultKey.String(result),
	)

	instruments.flowDuration.Record(ctx, durationMilliseconds(start), attrs)
	if err != nil {
		instruments.flowFailures.Add(ctx, 1, attrs)
	}
}

func observeLightningPhase(ctx context.Context, flow, phase string, start time.Time, err error) {
	instruments := getLightningMetricInstruments()
	result := classifyLightningMetricResult(err)

	instruments.phaseDuration.Record(
		ctx,
		durationMilliseconds(start),
		metric.WithAttributes(
			lightningFlowKey.String(flow),
			lightningPhaseKey.String(phase),
			lightningResultKey.String(result),
		),
	)
}

func observeOperatorFanoutRPC(ctx context.Context, operation, targetOperatorIdentifier string, start time.Time, err error) {
	instruments := getLightningMetricInstruments()
	result := classifyLightningMetricResult(err)
	attrs := metric.WithAttributes(
		lightningOperationKey.String(operation),
		lightningTargetOperatorIndexKey.String(lightningTargetOperatorIndex(targetOperatorIdentifier)),
		lightningResultKey.String(result),
	)

	instruments.operatorRPC.Record(ctx, durationMilliseconds(start), attrs)
	if err != nil {
		instruments.operatorRPCFailure.Add(ctx, 1, attrs)
	}
}

func durationMilliseconds(start time.Time) float64 {
	return float64(time.Since(start)) / float64(time.Millisecond)
}

func lightningTargetOperatorIndex(operatorIdentifier string) string {
	operatorIndexPlusOne, err := strconv.ParseUint(operatorIdentifier, 16, 64)
	if err != nil || operatorIndexPlusOne == 0 {
		return "unknown"
	}
	return strconv.FormatUint(operatorIndexPlusOne-1, 10)
}

func classifyLightningMetricResult(err error) string {
	if err == nil {
		return lightningResultSuccess
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return lightningResultTimeout
	}
	if errors.Is(err, context.Canceled) {
		return lightningResultCanceled
	}

	if code, ok := lightningGRPCStatusCode(err); ok {
		switch code {
		case codes.DeadlineExceeded:
			return lightningResultTimeout
		case codes.Canceled:
			return lightningResultCanceled
		case codes.Unavailable:
			return lightningResultUnavailable
		default:
			return lightningResultError
		}
	}

	return lightningResultError
}

type grpcStatusProvider interface {
	GRPCStatus() *status.Status
}

func lightningGRPCStatusCode(err error) (codes.Code, bool) {
	var grpcStatus grpcStatusProvider
	if !errors.As(err, &grpcStatus) {
		return codes.OK, false
	}
	status := grpcStatus.GRPCStatus()
	if status == nil {
		return codes.Unknown, true
	}
	return status.Code(), true
}
