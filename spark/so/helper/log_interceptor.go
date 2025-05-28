package helper

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

func LogInterceptor(enableStats bool) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Ignore health check requests, these are noisy and we don't care about logging them.
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health") {
			return handler(ctx, req)
		}

		requestID := uuid.New().String()

		var ip string
		if p, ok := peer.FromContext(ctx); ok {
			ip = p.Addr.String()
		}

		var traceID string
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if traceVals := md.Get("x-amzn-trace-id"); len(traceVals) > 0 {
				traceID = traceVals[0]
			}
		}

		var otelTraceID string
		span := trace.SpanFromContext(ctx)
		if span != nil {
			sc := span.SpanContext()
			if sc.HasTraceID() {
				otelTraceID = sc.TraceID().String()
			}
		}

		logger := slog.Default().With(
			"request_id", requestID,
			"method", info.FullMethod,
			"caller_ip", ip,
			"x_amzn_trace_id", traceID,
			"otel_trace_id", otelTraceID,
			"component", "grpc",
		)

		ctx = logging.Inject(ctx, logger)
		ctx = logging.InitTable(ctx)

		reqProto, ok := req.(proto.Message)
		if ok {
			logger.Info("grpc call started", "request", proto.MessageName(reqProto))
		}

		startTime := time.Now()
		response, err := handler(ctx, req)
		duration := time.Since(startTime)

		respProto, _ := response.(proto.Message)

		if enableStats {
			logging.LogTable(ctx, duration, reqProto, respProto, err)
		}

		if err != nil {
			logger.Error("error in grpc", "error", err, "duration", duration.Seconds())
		} else {
			responseProto, ok := response.(proto.Message)
			if ok {
				logger.Info("grpc call successful", "response", proto.MessageName(responseProto), "duration", duration.Seconds())
			}
		}

		return response, err
	}
}
