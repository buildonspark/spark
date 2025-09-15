package grpc

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/middleware"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

func LogInterceptor(rootLogger *zap.Logger, tableLogger *logging.TableLogger) grpc.UnaryServerInterceptor {
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

		logger := rootLogger.With(
			zap.String("request_id", requestID),
			zap.String("method", info.FullMethod),
			zap.String("x_amzn_trace_id", traceID),
			zap.String("otel_trace_id", otelTraceID),
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
			logger.Error("error in grpc", zap.Error(err))
		}

		return response, err
	}
}

type WrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func WrapServerStream(ctx context.Context, stream grpc.ServerStream) grpc.ServerStream {
	return &WrappedServerStream{
		ServerStream: stream,
		ctx:          ctx,
	}
}

func (w *WrappedServerStream) Context() context.Context {
	return w.ctx
}

func StreamLogInterceptor(rootLogger *zap.Logger) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		// Ignore health check requests, these are noisy and we don't care about logging them.
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health") {
			return handler(srv, ss)
		}

		requestID := uuid.New().String()

		logger := rootLogger.With(
			zap.String("request_id", requestID),
			zap.String("method", info.FullMethod),
		)

		ctx := logging.Inject(ss.Context(), logger)

		err := handler(srv, WrapServerStream(ctx, ss))
		if err != nil {
			logger.Error("error in grpc stream", zap.Error(err))
		}

		return err
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
