package grpc

import (
	"context"
	"sync"

	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Interface for a resource limiter that allows enforcing a budget on acquiring and releasing resources.
type ResourceLimiter interface {
	// Attempts to acquire a resource, throwing an error if the limit is reached.
	TryAcquire() error
	// Releases a resource, decrementing the current count.
	Release()
}

type ConcurrencyGuard struct {
	current      int64
	defaultLimit int
	mu           sync.Mutex
	knobsService knobs.Knobs
}

func NewConcurrencyGuard(knobsService knobs.Knobs, defaultLimit int) ResourceLimiter {
	return &ConcurrencyGuard{
		current:      int64(0),
		defaultLimit: defaultLimit,
		mu:           sync.Mutex{},
		knobsService: knobsService,
	}
}

// Acquire attempts to acquire a concurrency slot, throwing an error if the limit is reached.
// If the limit is 0, no limit is enforced.
// If the limit is negative, the default limit is used.
func (c *ConcurrencyGuard) TryAcquire() error {
	limit := int64(c.knobsService.GetValue(knobs.KnobGrpcServerConcurrencyLimitLimit, float64(c.defaultLimit)))
	if limit < 0 {
		limit = int64(c.defaultLimit)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if limit > 0 && c.current >= limit {
		return status.Errorf(codes.ResourceExhausted, "concurrency limit exceeded")
	}
	c.current++
	return nil
}

// Decrements the current resource count, freeing up a concurrency slot.
// Protected against going negative.
func (c *ConcurrencyGuard) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = max(c.current-1, 0)
}

// A no-op resource limiter that allows unlimited concurrency.
type NoopResourceLimiter struct{}

func (n *NoopResourceLimiter) TryAcquire() error {
	return nil
}

func (n *NoopResourceLimiter) Release() {
}

// Creates a unary server interceptor that enforces a concurrency limit on incoming gRPC requests
func ConcurrencyInterceptor(guard ResourceLimiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := guard.TryAcquire(); err != nil {
			return nil, err
		}
		defer guard.Release()
		return handler(ctx, req)
	}
}
