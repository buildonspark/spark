package helper

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type contextKey string

const LoggerKey = contextKey("logger")

func LogInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	requestID := uuid.New().String()
	logger := slog.Default().With("request_id", requestID, "method", info.FullMethod)
	ctx = context.WithValue(ctx, LoggerKey, logger)
	reqProto, ok := req.(proto.Message)
	if ok {
		logger.Info("grpc call started", "request", proto.MessageName(reqProto))
	}
	response, err := handler(ctx, req)
	if err != nil {
		logger.Error("error in grpc", "error", err)
	} else {
		responseProto, ok := response.(proto.Message)
		if ok {
			logger.Info("grpc call successful", "response", proto.MessageName(responseProto))
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
