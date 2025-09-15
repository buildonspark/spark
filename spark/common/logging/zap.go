package logging

import (
	"context"
	"runtime"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type loggerContextKey string

const loggerKey = loggerContextKey("logger")

// Inject the logger into the context. This should ONLY be called from the start of a request
// or worker context (e.g. in a top-level gRPC interceptor).
func Inject(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// Get an instance of zap.SugaredLogger from the current context. If no logger is found, returns a
// noop logger.
func GetLoggerFromContext(ctx context.Context) *zap.Logger {
	logger, ok := ctx.Value(loggerKey).(*zap.Logger)
	if !ok {
		return zap.NewNop()
	}
	return logger
}

func WithIdentityPubkey(ctx context.Context, pubKey keys.Public) (context.Context, *zap.Logger) {
	return WithAttrs(ctx, zap.Stringer("identity_public_key", pubKey))
}

func WithAttrs(ctx context.Context, fields ...zap.Field) (context.Context, *zap.Logger) {
	logger := GetLoggerFromContext(ctx).With(fields...)
	return Inject(ctx, logger), logger
}

// Custom core that automatically adds source information to every log entry
type SourceCore struct {
	zapcore.Core
}

func (s *SourceCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// Add source information
	if entry.Caller.Defined {
		pc := entry.Caller.PC
		fn := runtime.FuncForPC(pc)

		var functionName string
		if fn != nil {
			functionName = fn.Name()
		}

		sourceField := zap.Object("source", zapcore.ObjectMarshalerFunc(func(enc zapcore.ObjectEncoder) error {
			enc.AddString("function", functionName)
			enc.AddString("file", entry.Caller.File)
			enc.AddInt("line", entry.Caller.Line)
			return nil
		}))

		fields = append(fields, sourceField)
	}

	return s.Core.Write(entry, fields)
}

func (s *SourceCore) With(fields []zapcore.Field) zapcore.Core {
	return &SourceCore{Core: s.Core.With(fields)}
}

func (s *SourceCore) Check(entry zapcore.Entry, checkedEntry *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if s.Enabled(entry.Level) {
		return checkedEntry.AddCore(entry, s)
	}
	return checkedEntry
}
