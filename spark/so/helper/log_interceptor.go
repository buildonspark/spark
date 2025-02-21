package helper

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc"
)

type contextKey string

const LoggerKey = contextKey("logger")

func LogInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	requestID := uuid.New().String()
	logger := slog.Default().With("request_id", requestID, "method", info.FullMethod)
	ctx = context.WithValue(ctx, LoggerKey, logger)
	return handler(ctx, req)
}

func GetLoggerFromContext(ctx context.Context) *slog.Logger {
	logger, ok := ctx.Value(LoggerKey).(*slog.Logger)
	if !ok {
		return slog.Default()
	}
	return logger
}
