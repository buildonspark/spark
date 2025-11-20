package grpc

import (
	"context"
	"time"

	"github.com/lightsparkdev/spark/so/grpcutil"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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

		if msg, ok := req.(proto.Message); ok {
			size := proto.Size(msg)
			span.SetAttributes(attribute.Int("request.size_bytes", size))
		}

		if p, ok := peer.FromContext(ctx); ok {
			span.SetAttributes(
				attribute.String("client.addr", p.Addr.String()),
			)
		}

		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ua := md.Get("user-agent"); len(ua) > 0 {
				span.SetAttributes(attribute.String("http.user_agent", ua[0]))
			}
			if auth := md.Get(":authority"); len(auth) > 0 {
				span.SetAttributes(attribute.String("http.request.header.authority", auth[0]))
			}
			if enc := md.Get("grpc-encoding"); len(enc) > 0 {
				span.SetAttributes(attribute.String("rpc.grpc.request.encoding", enc[0]))
			}
		}

		return handler(ctx, req)
	}
}
