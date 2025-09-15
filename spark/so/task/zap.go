package task

import (
	"fmt"

	"go.uber.org/zap"
)

// ZapLoggerAdapter is an adapter that wraps a zap.Logger to implement the gocron.Logger interface.
type ZapLoggerAdapter struct {
	logger *zap.Logger
}

func NewZapLoggerAdapter(logger *zap.Logger) *ZapLoggerAdapter {
	return &ZapLoggerAdapter{logger: logger}
}

func (z *ZapLoggerAdapter) Debug(msg string, args ...any) {
	z.logger.Debug(z.formatMessage(msg, args...))
}

func (z *ZapLoggerAdapter) Info(msg string, args ...any) {
	z.logger.Info(z.formatMessage(msg, args...))
}

func (z *ZapLoggerAdapter) Warn(msg string, args ...any) {
	z.logger.Warn(z.formatMessage(msg, args...))
}

func (z *ZapLoggerAdapter) Error(msg string, args ...any) {
	z.logger.Error(z.formatMessage(msg, args...))
}

// Zap will include extra fields e.g. "name", singleton-27bee363-0dd5-4fd2-8e22-de14eec6fe87.
func (z *ZapLoggerAdapter) formatMessage(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}

	msg += " ("
	for i := 0; i < len(args)-1; i += 2 {
		if i != 0 {
			msg += ", "
		}

		msg += fmt.Sprint(args[i]) + ": " + fmt.Sprint(args[i+1])
	}
	msg += ")"

	return msg
}
