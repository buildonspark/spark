package helper

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

type contextKey string

const LoggerKey = contextKey("logger")

func LogInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
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

	logger := slog.Default().With(
		"request_id", requestID,
		"method", info.FullMethod,
		"caller_ip", ip,
		"x_amzn_trace_id", traceID,
	)

	ctx = context.WithValue(ctx, LoggerKey, logger)

	reqProto, ok := req.(proto.Message)
	if ok {
		logger.Info("grpc call started", "request", proto.MessageName(reqProto))
	}

	startTime := time.Now()
	response, err := handler(ctx, req)
	duration := time.Since(startTime).Seconds()

	if err != nil {
		logger.Error("error in grpc", "error", err, "duration", duration)
	} else {
		responseProto, ok := response.(proto.Message)
		if ok {
			logger.Info("grpc call successful", "response", proto.MessageName(responseProto), "duration", duration)
		}
	}

	return response, err
}

func GetLoggerFromContext(ctx context.Context) *slog.Logger {
	logger, ok := ctx.Value(LoggerKey).(*slog.Logger)
	if !ok {
		return slog.Default()
	}
	return logger
}
