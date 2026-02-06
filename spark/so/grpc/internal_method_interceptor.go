package grpc

import (
	"context"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/grpcutil"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InternalMethodInterceptor returns an interceptor that blocks internal API calls
// when the KnobGrpcServerInternalMethodEnabled knob is disabled for the method.
// This mirrors KnobGrpcServerMethodEnabled (used for public methods in the rate limiter)
// but is scoped to internal (SO-to-SO) services only.
//
// Usage: Set "spark.so.grpc.server.internal_method.enabled@/service/Method" to 0
// in the knobs ConfigMap to disable that internal method on all operators.
func InternalMethodInterceptor(protectedServices []string) grpc.UnaryServerInterceptor {
	protected := make(map[string]bool, len(protectedServices))
	for _, svc := range protectedServices {
		protected[svc] = true
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		service, _ := grpcutil.ParseFullMethodStrings(info.FullMethod)
		if !protected[service] {
			return handler(ctx, req)
		}

		knobsService := knobs.GetKnobsService(ctx)
		enabled := knobsService.GetValueTarget(knobs.KnobGrpcServerInternalMethodEnabled, &info.FullMethod, 100)

		if enabled <= 0 {
			logger := logging.GetLoggerFromContext(ctx)
			logger.Warn("InternalMethodInterceptor: blocking internal method",
				zap.String("method", info.FullMethod),
				zap.Float64("enabled_value", enabled))
			return nil, status.Error(codes.Unavailable, "internal method disabled")
		}

		return handler(ctx, req)
	}
}

// InternalMethodStreamInterceptor returns a stream interceptor that blocks internal API calls
// when the KnobGrpcServerInternalMethodEnabled knob is disabled for the method.
func InternalMethodStreamInterceptor(protectedServices []string) grpc.StreamServerInterceptor {
	protected := make(map[string]bool, len(protectedServices))
	for _, svc := range protectedServices {
		protected[svc] = true
	}

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		service, _ := grpcutil.ParseFullMethodStrings(info.FullMethod)
		if !protected[service] {
			return handler(srv, ss)
		}

		knobsService := knobs.GetKnobsService(ss.Context())
		enabled := knobsService.GetValueTarget(knobs.KnobGrpcServerInternalMethodEnabled, &info.FullMethod, 100)

		if enabled <= 0 {
			logger := logging.GetLoggerFromContext(ss.Context())
			logger.Warn("InternalMethodStreamInterceptor: blocking internal stream method",
				zap.String("method", info.FullMethod),
				zap.Float64("enabled_value", enabled))
			return status.Error(codes.Unavailable, "internal method disabled")
		}

		return handler(srv, ss)
	}
}
