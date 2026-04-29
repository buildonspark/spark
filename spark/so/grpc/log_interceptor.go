package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/middleware"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// isClientError returns true for gRPC status codes that represent client-side
// errors (bad input, expired tokens, etc.) rather than server failures. These
// must stay in sync with the "expected" codes in our Prometheus alerting rules
// (ops/helm/monitoring/files/alerts.yaml, spark-service-error-rate) so that
// Warn-level logs here don't mask errors that still trigger alerts.
func isClientError(err error) bool {
	code, _ := sparkerrors.CodeAndReasonFrom(err)
	switch code {
	case codes.InvalidArgument,
		codes.AlreadyExists,
		codes.ResourceExhausted,
		codes.FailedPrecondition,
		codes.Aborted,
		codes.Unauthenticated,
		codes.Canceled:
		return true
	default:
		return false
	}
}

// logGRPCError logs at Warn for client-caused errors, Error for server failures.
func logGRPCError(logger *zap.Logger, msg string, err error) {
	if isClientError(err) {
		logger.Warn(msg, zap.Error(err))
	} else {
		logger.Error(msg, zap.Error(err))
	}
}

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
		ctx = logging.InitRequestFields(ctx)

		startTime := time.Now()
		response, err := handler(ctx, req)
		duration := time.Since(startTime)

		reqProto, _ := req.(proto.Message)
		respProto, _ := response.(proto.Message)

		loggerWithAccumulatedRequestFields := logging.GetLoggerWithAccumulatedRequestFields(ctx)
		ctx = logging.Inject(ctx, loggerWithAccumulatedRequestFields)

		if tableLogger != nil {
			tableLogger.Log(ctx, duration, reqProto, respProto, err)
		}

		if err != nil {
			if isContextCanceled(err) {
				reason := contextCancelReason(ctx, duration)
				if isDeadlineExpired(ctx) {
					loggerWithAccumulatedRequestFields.Error("request deadline expired",
						zap.Error(err),
						zap.Duration("duration", duration),
						zap.String("cancel_reason", reason),
					)
				} else {
					loggerWithAccumulatedRequestFields.Warn("request canceled",
						zap.Error(err),
						zap.Duration("duration", duration),
						zap.String("cancel_reason", reason),
					)
				}
			} else {
				logGRPCError(loggerWithAccumulatedRequestFields, "error in grpc", err)
			}
		}

		return response, err
	}
}

func isContextCanceled(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if st, ok := status.FromError(err); ok && (st.Code() == codes.Canceled || st.Code() == codes.DeadlineExceeded) {
		return true
	}
	return false
}

func isDeadlineExpired(ctx context.Context) bool {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline) <= 0
	}
	return false
}

func contextCancelReason(ctx context.Context, duration time.Duration) string {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return fmt.Sprintf("cause: %v", cause)
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Sprintf("deadline expired %v ago (elapsed: %v)", -remaining, duration)
		}
		return fmt.Sprintf("canceled with %v remaining of deadline (elapsed: %v)", remaining, duration)
	}
	return fmt.Sprintf("no deadline set (elapsed: %v)", duration)
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
		ctx = logging.InitRequestFields(ctx)

		err := handler(srv, WrapServerStream(ctx, ss))

		loggerWithAccumulatedRequestFields := logging.GetLoggerWithAccumulatedRequestFields(ctx)
		if err != nil && !errors.Is(err, sparkerrors.ErrShuttingDown) {
			logGRPCError(loggerWithAccumulatedRequestFields, "error in grpc stream", err)
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
	if ip := middleware.GetClientIP(ctx, g.xffClientIpPosition); ip != "" {
		return ip, nil
	}
	return "", sparkerrors.InternalObjectMissingField(fmt.Errorf("no client IP found in header or peer context"))
}
