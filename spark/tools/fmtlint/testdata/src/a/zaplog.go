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

// Print-style methods called with format verbs and further arguments should be the printf variant.
func _zapPrintStyle(logger *zap.SugaredLogger) {
	var v valStringer

	logger.Info("querying transfer with ID: %s", v) // want `Info call has format directive %s: use Infof`
	logger.Error("failed after %d attempts", 3)     // want `Error call has format directive %d: use Errorf`
	logger.Warn("states %v and %q", v, v)           // want `Warn call has format directive %v: use Warnf`

	// Log takes the level before the variadic slot, so the message is at index 1 — the check must find it there, and
	// must not misread the argument after the message as the template.
	logger.Log(zap.InfoLevel, "failed to sync %s", v) // want `Log call has format directive %s: use Logf`
	logger.Logf(zap.InfoLevel, "got %s", v.String())  // want `unnecessary .String`

	// Not flagged: no verbs, Sprint-style concatenation.
	logger.Info("querying transfer with ID:", v)
	// Not flagged: a verb with no arguments after the string prints literally, which may be intentional.
	logger.Debug("template is %s")
	// Not flagged: escaped percent is not a directive.
	logger.Info("100%% done", v)
	// Not flagged: the prose percent parses as a space-flagged %d, which isn't treated as evidence of formatting.
	logger.Info("progress: 100% done", v)
	// Not flagged: '%' before a non-verb byte makes the parse unreliable, so the string is treated as prose.
	logger.Info("improved by 5%!", v)
}
