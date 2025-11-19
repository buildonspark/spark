package grpc

import (
	"context"
	"time"

	"github.com/lightsparkdev/spark/so/grpcutil"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

func TracingInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorStartTime := time.Now()

		ctx, span := tracer.Start(ctx, "TracingInterceptor")
		defer span.End()

		// Add RPC method attributes for collector filtering
		if attrs := grpcutil.ParseFullMethod(info.FullMethod); attrs != nil {
			span.SetAttributes(attrs...)
		}

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

		// Add client address to identify problematic clients
		if p, ok := peer.FromContext(ctx); ok {
			span.SetAttributes(attribute.String("client.addr", p.Addr.String()))
		}

		return handler(ctx, req)
	}
}
