package handler

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const (
	lightningFlowStorePreimageShare = "store_preimage_share"
	lightningFlowProvidePreimage    = "provide_preimage"

	lightningResultSuccess = "success"
	lightningResultError   = "failure"

	lightningPhaseConsensusExecute = "consensus_execute"
	lightningPhaseCoordinatorStore = "coordinator_store"
	lightningPhaseStorePreimage    = "store_preimage"
	lightningPhaseFanout           = "fanout"
	lightningPhaseSendGossip       = "send_gossip"
	lightningPhaseReloadTransfer   = "reload_transfer"
	lightningPhaseMarshalTransfer  = "marshal_transfer"
)

var (
	lightningFlowKey   = attribute.Key("flow")
	lightningPhaseKey  = attribute.Key("phase")
	lightningResultKey = attribute.Key("result")
)

var lightningMetricBuckets = []float64{
	100, 500, 1000, 5000, 15000, 30000,
}

type lightningMetricInstruments struct {
	phaseDuration metric.Float64Histogram
}

var getLightningMetricInstruments = sync.OnceValue(func() *lightningMetricInstruments {
	meter := otel.Meter("handler.lightning")

	phaseDuration, err := meter.Float64Histogram(
		"spark_lightning_flow_phase_duration_milliseconds",
		metric.WithDescription("Duration of Lightning flow phases"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(lightningMetricBuckets...),
	)
	if err != nil {
		phaseDuration = noop.Float64Histogram{}
	}

	return &lightningMetricInstruments{
		phaseDuration: phaseDuration,
	}
})

func observeLightningPhase(ctx context.Context, flow, phase string, start time.Time, err error) {
	instruments := getLightningMetricInstruments()
	result := lightningResultSuccess
	if err != nil {
		result = lightningResultError
	}

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

func durationMilliseconds(start time.Time) float64 {
	return float64(time.Since(start)) / float64(time.Millisecond)
}
