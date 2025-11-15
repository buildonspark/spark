package grpc

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TracingInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorStartTime := time.Now()

		ctx, span := tracer.Start(ctx, "TracingInterceptor")
		defer span.End()

		// Calculate gap from TagRPC to interceptor chain start
		if timings, ok := ctx.Value(RPCTimingsContextKey).(*RPCTimings); ok && timings != nil {
			gapMs := interceptorStartTime.Sub(timings.TagRPCTime).Milliseconds()
			span.SetAttributes(attribute.Int64("gap_from_tagrpc_ms", gapMs))
		}

		// Add request size to correlate with gaps
		if msg, ok := req.(proto.Message); ok {
			size := proto.Size(msg)
			span.SetAttributes(attribute.Int("request.size_bytes", size))
		}

		return handler(ctx, req)
	}
}
