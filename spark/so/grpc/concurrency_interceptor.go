package grpc

import (
	"context"
	"sync"

	"github.com/lightsparkdev/spark/so/grpcutil"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Interface for a resource limiter that allows enforcing a budget on acquiring and releasing resources.
type ResourceLimiter interface {
	// Attempts to acquire a resource, throwing an error if the limit is reached.
	TryAcquireMethod(string) error
	// Releases a resource, decrementing the current count.
	ReleaseMethod(string)
}

type ConcurrencyGuard struct {
	// Current count of acquired resources overall.
	globalCounter int64
	// A map of gRPC method names to their current count of acquired resources.
	counterMap map[string]int64
	// The maximum number of resources that can be acquired overall.
	defaultGlobalLimit int64
	// A mutex for synchronizing access to the counter map.
	mu sync.Mutex
	// A knobs service for retrieving limit overrides.
	knobsService knobs.Knobs
}

func NewConcurrencyGuard(knobsService knobs.Knobs, defaultGlobalLimit int64) ResourceLimiter {
	return &ConcurrencyGuard{
		globalCounter:      0,
		counterMap:         make(map[string]int64),
		defaultGlobalLimit: defaultGlobalLimit,
		mu:                 sync.Mutex{},
		knobsService:       knobsService,
	}
}

// Attempts to acquire a concurrency slot for a gRPC method AND the global limit, throwing an error if either limit is reached.
// If the limit is 0, no limit is enforced.
// If the limit is negative, the default limit is used.
func (c *ConcurrencyGuard) TryAcquireMethod(method string) error {
	methodLimit := int64(c.knobsService.GetValueTarget(knobs.KnobGrpcServerConcurrencyLimitLimit, &method, -1))

	globalLimit := int64(c.knobsService.GetValue(knobs.KnobGrpcServerConcurrencyLimitLimit, float64(c.defaultGlobalLimit)))

	c.mu.Lock()
	defer c.mu.Unlock()

	currentCounter, loaded := c.counterMap[method]
	if !loaded {
		currentCounter = 0
	}

	// Acquire a slot for the method.
	if methodLimit > 0 && currentCounter >= methodLimit {
		return status.Errorf(codes.ResourceExhausted, "concurrency limit exceeded")
	}

	// Acquire a slot for the global limit.
	if globalLimit > 0 && c.globalCounter >= globalLimit {
		return status.Errorf(codes.ResourceExhausted, "global concurrency limit exceeded")
	}

	c.counterMap[method] = currentCounter + 1
	c.globalCounter++

	return nil
}

// Decrements the current resource count for a gRPC method, freeing up a concurrency slot.
// Protected against going negative.
func (c *ConcurrencyGuard) ReleaseMethod(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	currentCounter, loaded := c.counterMap[method]
	if !loaded {
		currentCounter = 0
	}
	c.counterMap[method] = max(currentCounter-1, 0)

	c.globalCounter = max(c.globalCounter-1, 0)
}

// A no-op resource limiter that allows unlimited concurrency.
type NoopResourceLimiter struct{}

func (n *NoopResourceLimiter) TryAcquireMethod(string) error {
	return nil
}

func (n *NoopResourceLimiter) ReleaseMethod(string) {
}

var (
	methodConcurrencyGauge metric.Int64UpDownCounter
)

func init() {
	meter := otel.GetMeterProvider().Meter("spark.grpc")
	newMethodConcurrencyGauge, err := meter.Int64UpDownCounter(
		"rpc.server.active_requests_per_rpc",
		metric.WithDescription("Number of currently active gRPCrequests"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		newMethodConcurrencyGauge = noop.Int64UpDownCounter{}
	}
	methodConcurrencyGauge = newMethodConcurrencyGauge
}

// Creates a unary server interceptor that enforces a concurrency limit on incoming gRPC requests
func ConcurrencyInterceptor(guard ResourceLimiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := guard.TryAcquireMethod(info.FullMethod); err != nil {
			return nil, err
		}
		defer guard.ReleaseMethod(info.FullMethod)

		otelAttrs := metric.WithAttributes(grpcutil.ParseFullMethod(info.FullMethod)...)
		methodConcurrencyGauge.Add(ctx, 1, otelAttrs)
		defer methodConcurrencyGauge.Add(ctx, -1, otelAttrs)

		return handler(ctx, req)
	}
}
