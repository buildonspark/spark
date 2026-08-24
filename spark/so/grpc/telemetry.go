package grpc

import (
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

var tracer = otel.Tracer("grpc")

// Instrument names otelgrpc records for RPC duration. Since otelgrpc adopted
// the stable RPC semantic conventions these are `*.call.duration`, measured in
// seconds; the older `rpc.{server,client}.duration` (milliseconds) is emitted
// only under OTEL_SEMCONV_STABILITY_OPT_IN=rpc/old|rpc/dup.
const (
	serverCallDurationInstrument = "rpc.server.call.duration"
	clientCallDurationInstrument = "rpc.client.call.duration"
)

// Default gRPC OTEL histogram boundaries top out at 10s; some of our calls are
// going longer than that. We top out at 180s because that's our current client
// and server timeouts.
var grpcLatencyBucketsSeconds = []float64{
	0, 0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1,
	2.5, 5, 7.5, 10, 15, 30, 60, 90, 120, 180,
}

// LatencyViews widens the gRPC duration histograms past the OTEL defaults.
//
// A view whose instrument name matches nothing is silently inert, so these
// names are load-bearing: if otelgrpc renames or re-units its duration
// instruments again, the views stop applying and every latency percentile
// above the default 10s ceiling silently becomes unresolvable. TestLatencyViews
// exercises a real RPC to catch exactly that.
func LatencyViews() []sdkmetric.View {
	widen := func(name string) sdkmetric.View {
		return sdkmetric.NewView(
			sdkmetric.Instrument{Name: name},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: grpcLatencyBucketsSeconds,
			}},
		)
	}
	return []sdkmetric.View{
		widen(serverCallDurationInstrument),
		widen(clientCallDurationInstrument),
	}
}
