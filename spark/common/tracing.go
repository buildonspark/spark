package common

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc/credentials"
)

const (
	// ServiceName is the static name of our service
	ServiceName = "spark-operator"
)

// TracingConfig holds configuration for OpenTelemetry tracing
type TracingConfig struct {
	// Enabled determines if tracing is enabled
	Enabled bool `yaml:"enabled"`
	// OTelCollectorEndpoint is the endpoint for the OpenTelemetry collector
	OTelCollectorEndpoint string `yaml:"otel_collector_endpoint"`
	// OTelCollectorCertPath is the path to the OpenTelemetry collector certificate
	OTelCollectorCertPath string `yaml:"otel_collector_cert_path"`
	// GlobalSamplingRate is the default sampling rate for all spans (0.0 to 1.0)
	GlobalSamplingRate float64 `yaml:"global_sampling_rate"`
	// SpanSamplingConfig contains per-span sampling configuration
	SpanSamplingConfig SpanSamplingConfig `yaml:"span_sampling_config"`
}

// SpanSamplingConfig contains configuration for span-specific sampling
type SpanSamplingConfig struct {
	// PerSpanSamplingRates allows setting specific sampling rates for specific spans
	// Key is the span name, value is the sampling rate (0.0 to 1.0)
	PerSpanSamplingRates map[string]float64 `yaml:"per_span_sampling_rates"`
	// AllowList if not empty, only spans in this list will be sampled
	AllowList []string `yaml:"allow_list"`
	// BlockList spans in this list will never be sampled
	BlockList []string `yaml:"block_list"`
}

// ConfigureTracing sets up the complete tracing configuration including sampling and exporters
// Returns a shutdown function that should be deferred by the caller
func ConfigureTracing(ctx context.Context, config TracingConfig) (func(context.Context) error, error) {
	slog.Info("Configuring tracing", "endpoint", config.OTelCollectorEndpoint)

	certPool := x509.NewCertPool()
	collectorCert, err := os.ReadFile(config.OTelCollectorCertPath)
	if err != nil {
		return nil, err
	}
	if !certPool.AppendCertsFromPEM(collectorCert) {
		return nil, errors.New("failed to append certificate")
	}

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(config.OTelCollectorEndpoint),
		otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(certPool, "")),
		otlptracegrpc.WithTimeout(5*time.Second),
	)
	if err != nil {
		return nil, err
	}

	sampler := trace.TraceIDRatioBased(config.GlobalSamplingRate)

	// If we have span-specific configs, wrap with our custom sampler
	if len(config.SpanSamplingConfig.PerSpanSamplingRates) > 0 ||
		len(config.SpanSamplingConfig.AllowList) > 0 ||
		len(config.SpanSamplingConfig.BlockList) > 0 {
		sampler = &customSampler{
			baseSampler:     sampler,
			spanConfig:      config.SpanSamplingConfig,
			allowListActive: len(config.SpanSamplingConfig.AllowList) > 0,
		}
	}

	resource, err := resource.Merge(resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(ServiceName),
	), resource.Environment())
	if err != nil {
		return nil, fmt.Errorf("failed to merge OpenTelemetry resources: %w", err)
	}

	// Create the TracerProvider with all configuration
	tp := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter,
			trace.WithBatchTimeout(10*time.Second),
			trace.WithMaxExportBatchSize(1000),
		),
		trace.WithSampler(trace.ParentBased(sampler)),
		trace.WithResource(resource),
	)

	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// customSampler implements trace.Sampler interface with advanced filtering
type customSampler struct {
	baseSampler     trace.Sampler
	spanConfig      SpanSamplingConfig
	allowListActive bool
}

func (s *customSampler) ShouldSample(p trace.SamplingParameters) trace.SamplingResult {
	// Check blocklist first
	for _, blocked := range s.spanConfig.BlockList {
		if blocked == p.Name {
			return trace.SamplingResult{Decision: trace.Drop}
		}
	}

	if s.allowListActive {
		allowed := false
		for _, allowedSpan := range s.spanConfig.AllowList {
			if allowedSpan == p.Name {
				allowed = true
				break
			}
		}
		if !allowed {
			return trace.SamplingResult{Decision: trace.Drop}
		}
	}

	if rate, exists := s.spanConfig.PerSpanSamplingRates[p.Name]; exists {
		return trace.TraceIDRatioBased(rate).ShouldSample(p)
	}

	return s.baseSampler.ShouldSample(p)
}

func (s *customSampler) Description() string {
	return "CustomSampler"
}
