package grpc

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

// TestLatencyViews drives a real RPC through otelgrpc and asserts our widened
// boundaries actually reached the recorded histogram.
//
// Asserting on the view definitions alone would pass even when they match no
// instrument, which is the failure that matters: otelgrpc v0.70.0 renamed its
// duration instruments to the stable semantic conventions, the views silently
// stopped applying, and every latency percentile above the default 10s ceiling
// became unresolvable in production.
func TestLatencyViews(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(LatencyViews()...),
	)

	lis := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.StatsHandler(
		otelgrpc.NewServerHandler(otelgrpc.WithMeterProvider(provider)),
	))
	healthpb.RegisterHealthServer(server, health.NewServer())
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(otelgrpc.WithMeterProvider(provider))),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx := t.Context()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &collected))

	for _, instrument := range []string{serverCallDurationInstrument, clientCallDurationInstrument} {
		boundaries, found := histogramBoundaries(collected, instrument)
		require.True(t, found,
			"otelgrpc recorded no %q histogram; it likely renamed or re-united its duration "+
				"instruments, which leaves LatencyViews inert", instrument)
		assert.Equal(t, grpcLatencyBucketsSeconds, boundaries,
			"%s did not get our widened boundaries — the view matched nothing", instrument)
	}
}

func histogramBoundaries(rm metricdata.ResourceMetrics, name string) ([]float64, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok || len(hist.DataPoints) == 0 {
				return nil, false
			}
			return hist.DataPoints[0].Bounds, true
		}
	}
	return nil, false
}
