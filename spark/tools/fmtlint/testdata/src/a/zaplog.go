package a

import (
	"encoding/hex"

	"go.uber.org/zap"
)

// Zap's SugaredLogger printf methods are the codebase's primary logging path. These cases confirm the linter resolves
// the hand-written (*go.uber.org/zap.SugaredLogger).*f keys in formatFuncs and fires on both fix patterns.
func _zaplog(logger *zap.SugaredLogger) {
	var v valStringer // value-receiver Stringer, declared in a.go
	b := []byte{0xde, 0xad}

	logger.Debugf("got %s", v.String())               // want `unnecessary .String`
	logger.Infof("got %v", v.String())                // want `unnecessary .String`
	logger.Warnf("got %q", v.String())                // want `unnecessary .String`
	logger.Errorf("hex is %s", hex.EncodeToString(b)) // want `unnecessary hex.EncodeToString`

	// Correct usage: the value is passed directly, so nothing to flag.
	logger.Infof("%s", v)
}
