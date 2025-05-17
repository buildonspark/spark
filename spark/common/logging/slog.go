package logging

import (
	"context"
	"encoding/hex"
	"log/slog"
)

type loggerContextKey string

const loggerKey = loggerContextKey("slog")

// Inject the logger into the context. This should ONLY be called from the start of a request
// or worker context (e.g. in a top-level gRPC interceptor).
func Inject(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

func WithIdentityPubkey(ctx context.Context, pubkey []byte) context.Context {
	if pubkey == nil {
		return ctx
	}

	logger := GetLoggerFromContext(ctx).With("identity_public_key", Pubkey{Pubkey: pubkey})
	return Inject(ctx, logger)
}

// Get an instance of slog.Logger from the current context. If no logger is found, returns a
// default logger.
func GetLoggerFromContext(ctx context.Context) *slog.Logger {
	logger, ok := ctx.Value(loggerKey).(*slog.Logger)
	if !ok {
		return slog.Default()
	}
	return logger
}

type Pubkey struct{ Pubkey []byte }

func (l Pubkey) LogValue() slog.Value {
	return slog.AnyValue(hex.EncodeToString(l.Pubkey))
}
