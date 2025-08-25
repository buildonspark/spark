package common

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// This interface allows the common package to be independent of specific configuration
// systems like knobs while still supporting dynamic timeout configuration.
type TimeoutProvider interface {
	// GetTimeoutForMethod returns the timeout duration for a specific gRPC method.
	// If no specific timeout is configured for the method, the provider's default timeout is returned.
	GetTimeoutForMethod(method string) time.Duration
}

func ClientTimeoutInterceptor(timeoutProvider TimeoutProvider) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Get timeout for this specific method from the provider
		timeout := timeoutProvider.GetTimeoutForMethod(method)

		if timeout <= 0 {
			// If timeout is not set or is non-positive, proceed without timeout
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		return invoker(timeoutCtx, method, req, reply, cc, opts...)
	}
}
