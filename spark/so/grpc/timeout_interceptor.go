package grpc

import (
	"context"
	"time"

	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TimeoutInterceptor creates a unary server interceptor that enforces a timeout on incoming requests
func TimeoutInterceptor(knobsService knobs.Knobs, defaultServerUnaryHandlerTimeout time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		timeout := knobs.GetTargetDurationSeconds(knobsService, knobs.KnobGrpcServerUnaryHandlerTimeout, &info.FullMethod, defaultServerUnaryHandlerTimeout)
		if timeout.Seconds() <= 0 {
			// If timeout is not set or is non-positive, proceed without timeout
			return handler(ctx, req)
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		type result struct {
			resp any
			err  error
		}

		// Execute the handler in a goroutine to allow for timeout handling
		resultChan := make(chan result, 1)
		go func() {
			resp, err := handler(timeoutCtx, req)
			resultChan <- result{resp: resp, err: err}
		}()

		select {
		case res := <-resultChan:
			return res.resp, res.err
		case <-timeoutCtx.Done():
			if timeoutCtx.Err() == context.DeadlineExceeded {
				return nil, status.Errorf(codes.DeadlineExceeded, "request timeout after %v", timeout)
			}

			return nil, timeoutCtx.Err()
		}
	}
}
