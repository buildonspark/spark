// Package zap is a minimal stub of go.uber.org/zap's SugaredLogger, providing just enough of the printf-style API for
// the fmtlint analysis test to resolve calls to (*zap.SugaredLogger).Infof and friends.
package zap

type SugaredLogger struct{}

func (*SugaredLogger) Debugf(template string, args ...interface{}) {}
func (*SugaredLogger) Infof(template string, args ...interface{})  {}
func (*SugaredLogger) Warnf(template string, args ...interface{})  {}
func (*SugaredLogger) Errorf(template string, args ...interface{}) {}
