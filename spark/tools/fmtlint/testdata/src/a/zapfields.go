package a

import (
	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"
)

// zap.String with an eagerly-built Stringer result should be zap.Stringer, which defers the call until the entry is
// encoded.
func _zapFields() {
	var v valStringer
	var p ptrStringer
	var e errStringer
	pub := keys.GeneratePrivateKey().Public()

	_ = zap.String("transfer_id", v.String())  // want `unnecessary eager .String`
	_ = zap.String("public_key", pub.ToHex())  // want `unnecessary eager .ToHex`
	_ = zap.String("public_key", pub.String()) // want `unnecessary eager .String`

	// Flagged even though errStringer also implements error: fmt's Formatter/error precedence only applies to the verb
	// checks, while zap.Stringer calls String() directly.
	_ = zap.String("err_ish", e.String()) // want `unnecessary eager .String`

	// Not flagged: ptrStringer's String has a pointer receiver, so the value doesn't implement fmt.Stringer and can't
	// be passed to zap.Stringer.
	_ = zap.String("ptr", p.String())
	// Not flagged: a plain string value.
	_ = zap.String("plain", "hello")
}
