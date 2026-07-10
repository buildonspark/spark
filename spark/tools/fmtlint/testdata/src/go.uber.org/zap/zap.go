// Package zap is a minimal stub of go.uber.org/zap's SugaredLogger, providing just enough of the printf-style API for
// the fmtlint analysis test to resolve calls to (*zap.SugaredLogger).Infof and friends.
package zap

type SugaredLogger struct{}

// Level stands in for zapcore.Level. Log and Logf carry it before the message, unlike the per-level methods, which
// exercises the message-index computation for a variadic slot that isn't the first parameter.
type Level int8

const InfoLevel Level = 0

func (*SugaredLogger) Log(lvl Level, args ...interface{})                   {}
func (*SugaredLogger) Logf(lvl Level, template string, args ...interface{}) {}

func (*SugaredLogger) Debugf(template string, args ...interface{}) {}
func (*SugaredLogger) Infof(template string, args ...interface{})  {}
func (*SugaredLogger) Warnf(template string, args ...interface{})  {}
func (*SugaredLogger) Errorf(template string, args ...interface{}) {}

func (*SugaredLogger) Debug(args ...interface{}) {}
func (*SugaredLogger) Info(args ...interface{})  {}
func (*SugaredLogger) Warn(args ...interface{})  {}
func (*SugaredLogger) Error(args ...interface{}) {}

type Field struct{}

func String(key string, val string) Field { return Field{} }

// Stringer has a parameter that's declared structurally rather than as fmt.Stringer, matching the real signature without
// pulling fmt into the stub.
func Stringer(key string, val interface{ String() string }) Field { return Field{} }
