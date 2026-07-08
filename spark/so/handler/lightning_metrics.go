package handler

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	pbspark "github.com/lightsparkdev/spark/proto/spark"
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
	lightningShapeKey               = attribute.Key("shape")
	lightningReasonKey              = attribute.Key("reason")
	lightningEndpointKey            = attribute.Key("endpoint")
	lightningLegacyIgnoredKey       = attribute.Key("legacy_transfer_ignored")
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
	preimageSwapShape  metric.Int64Counter
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

	preimageSwapShape, err := meter.Int64Counter(
		"spark_lightning_preimage_swap_shape_total",
		metric.WithDescription("InitiatePreimageSwap requests by which transfer shape the caller sent; watch transfer_only/both with result=success drain to zero before removing the legacy transfer field"),
		metric.WithUnit("1"),
	)
	if err != nil {
		otel.Handle(err)
		preimageSwapShape = noop.Int64Counter{}
	}

	return &lightningMetricInstruments{
		flowDuration:       flowDuration,
		flowFailures:       flowFailures,
		phaseDuration:      phaseDuration,
		operatorRPC:        operatorRPC,
		operatorRPCFailure: operatorRPCFailure,
		preimageSwapShape:  preimageSwapShape,
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

// Request-shape labels for the legacy-transfer-object drain metric.
const (
	preimageSwapShapeTransferOnly        = "transfer_only"
	preimageSwapShapeTransferRequestOnly = "transfer_request_only"
	preimageSwapShapeBoth                = "both"
	preimageSwapShapeNeither             = "neither"
)

// preimageSwapShape labels a request by which transfer shape(s) it carries.
// Shapeless (always-rejected) requests get their own label so retries from a
// broken caller can't keep the watched transfer_only bucket nonzero.
// TODO(SP-3285): drain scaffolding — remove with the legacy transfer field.
func preimageSwapShape(req *pbspark.InitiatePreimageSwapRequest) string {
	switch {
	case req.GetTransferRequest() != nil && req.GetTransfer() != nil:
		return preimageSwapShapeBoth
	case req.GetTransferRequest() != nil:
		return preimageSwapShapeTransferRequestOnly
	case req.GetTransfer() != nil:
		return preimageSwapShapeTransferOnly
	default:
		return preimageSwapShapeNeither
	}
}

// preimageSwapReason collapses the open proto3 enum to a closed label set —
// callers control the wire value, and String() on an unknown value would mint
// unbounded metric cardinality.
// TODO(SP-3285): drain scaffolding — remove with the legacy transfer field.
func preimageSwapReason(reason pbspark.InitiatePreimageSwapRequest_Reason) string {
	switch reason {
	case pbspark.InitiatePreimageSwapRequest_REASON_SEND:
		return "send"
	case pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE:
		return "receive"
	default:
		return "unknown"
	}
}

// observePreimageSwapShape records at flow completion so the drain read can
// key on result=success; endpoint ("v2"/"v3") names which caller population
// still sends the legacy field. shape is the as-received shape (captured before
// KnobPreimageSwapIgnoreLegacyTransfer may strip it) so the drain census stays
// truthful; legacyTransferIgnored marks the requests that knob forced onto the
// transfer_request path — its result= split is the SEND package-path bake signal.
// TODO(SP-3285): drain scaffolding — remove with the legacy transfer field.
func observePreimageSwapShape(ctx context.Context, req *pbspark.InitiatePreimageSwapRequest, shape string, legacyTransferIgnored bool, endpoint string, err error) {
	getLightningMetricInstruments().preimageSwapShape.Add(ctx, 1, metric.WithAttributes(
		lightningShapeKey.String(shape),
		lightningReasonKey.String(preimageSwapReason(req.GetReason())),
		lightningEndpointKey.String(endpoint),
		lightningLegacyIgnoredKey.Bool(legacyTransferIgnored),
		lightningResultKey.String(classifyLightningMetricResult(err)),
	))
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
