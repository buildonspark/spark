package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/middleware"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

func LogInterceptor(tableLogger *logging.TableLogger) grpc.UnaryServerInterceptor {
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
			"x_amzn_trace_id", traceID,
			"otel_trace_id", otelTraceID,
			"component", "grpc",
		)

		ctx = logging.Inject(ctx, logger)
		ctx = logging.InitTable(ctx)

		startTime := time.Now()
		response, err := handler(ctx, req)
		duration := time.Since(startTime)

		reqProto, _ := req.(proto.Message)
		respProto, _ := response.(proto.Message)

		if tableLogger != nil {
			tableLogger.Log(ctx, duration, reqProto, respProto, err)
		}

		if err != nil {
			logger.Error("error in grpc", "error", err, "duration", duration.Seconds())
		}

		return response, err
	}
}

type GRPCClientInfoProvider struct {
	xffClientIpPosition int
}

func NewGRPCClientInfoProvider(xffClientIpPosition int) *GRPCClientInfoProvider {
	return &GRPCClientInfoProvider{
		xffClientIpPosition: xffClientIpPosition,
	}
}

func (g *GRPCClientInfoProvider) GetClientIP(ctx context.Context) (string, error) {
	if clientIP, err := middleware.GetClientIpFromHeader(ctx, g.xffClientIpPosition); err == nil {
		return clientIP, nil
	}

	// If we can't get the client IP from the header, just fall back to the peer.
	if p, ok := peer.FromContext(ctx); ok {
		if ip, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			return ip, nil
		} else {
			return p.Addr.String(), nil
		}
	}

	return "", fmt.Errorf("no client IP found in header or peer context")
}
