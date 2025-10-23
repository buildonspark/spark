package logging

import (
	"context"
	"runtime"
	"sync"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type loggerContextKey string

const loggerKey = loggerContextKey("logger")

type requestFieldsContextKey string

const requestFieldsKey = requestFieldsContextKey("requestFields")

type requestFields struct {
	fields []zap.Field
	mu     sync.Mutex
}

// Inject the logger into the context. This should ONLY be called from the start of a request
// or worker context (e.g. in a top-level gRPC interceptor).
func Inject(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

func InitRequestFields(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestFieldsKey, &requestFields{
		fields: make([]zap.Field, 0),
		mu:     sync.Mutex{},
	})
}

func addRequestFields(ctx context.Context, fields ...zap.Field) {
	fieldsContainer, ok := ctx.Value(requestFieldsKey).(*requestFields)
	if !ok {
		return
	}
	fieldsContainer.mu.Lock()
	defer fieldsContainer.mu.Unlock()
	fieldsContainer.fields = append(fieldsContainer.fields, fields...)
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

// GetLoggerWithAccumulatedRequestFields returns a logger with all fields that have been accumulated
// via WithRequestAttrs during the request lifecycle. This is primarily intended for use in the
// LogInterceptor to ensure table logging includes fields added by later interceptors.
func GetLoggerWithAccumulatedRequestFields(ctx context.Context) *zap.Logger {
	logger := GetLoggerFromContext(ctx)

	fieldsContainer, ok := ctx.Value(requestFieldsKey).(*requestFields)
	if !ok || fieldsContainer == nil {
		return logger
	}

	fieldsContainer.mu.Lock()
	defer fieldsContainer.mu.Unlock()

	if len(fieldsContainer.fields) == 0 {
		return logger
	}

	return logger.With(fieldsContainer.fields...)
}

func WithIdentityPubkey(ctx context.Context, pubKey keys.Public) (context.Context, *zap.Logger) {
	return WithRequestAttrs(ctx, zap.Stringer("identity_public_key", pubKey))
}

// WithAttrs adds fields to the logger in the context. These fields are NOT included in
// accumulated fields for table logging - use WithRequestAttrs for that.
func WithAttrs(ctx context.Context, fields ...zap.Field) (context.Context, *zap.Logger) {
	logger := GetLoggerFromContext(ctx).With(fields...)
	return Inject(ctx, logger), logger
}

// WithRequestAttrs adds fields to both the logger in the context AND the accumulated
// fields container. These fields will be included in table logging at the end of the request.
// Use this for important request-level fields like identity_public_key.
func WithRequestAttrs(ctx context.Context, fields ...zap.Field) (context.Context, *zap.Logger) {
	addRequestFields(ctx, fields...)
	return WithAttrs(ctx, fields...)
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
