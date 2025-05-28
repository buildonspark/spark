package common

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestConfigureTracing_SetsTracerProviderAndReturnsShutdown(t *testing.T) {
	origProvider := otel.GetTracerProvider()

	testCases := []struct {
		name string
		cfg  TracingConfig
	}{
		{
			name: "NoEndpoint",
			cfg: TracingConfig{
				Enabled:               true,
				OTelCollectorEndpoint: "",
				OTelCollectorCertPath: "",
				GlobalSamplingRate:    1.0,
			},
		},
		{
			name: "WithEndpoint",
			cfg: TracingConfig{
				Enabled:               true,
				OTelCollectorEndpoint: "localhost:4317",
				OTelCollectorCertPath: "/nonexistent/path/to/cert.pem",
				GlobalSamplingRate:    1.0,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset provider for each subtest
			otel.SetTracerProvider(origProvider)
			t.Cleanup(func() { otel.SetTracerProvider(origProvider) })

			shutdown, err := ConfigureTracing(context.Background(), tc.cfg)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if shutdown == nil {
				t.Fatalf("expected shutdown func, got nil")
			}

			tp := otel.GetTracerProvider()
			if tp == origProvider {
				t.Fatalf("expected tracer provider to be replaced")
			}
		})
	}
}
