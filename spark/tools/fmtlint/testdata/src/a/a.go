package a

import (
	"encoding/hex"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
)

// valStringer has a value-receiver String() string: both T and *T implement fmt.Stringer.
type valStringer struct{}

func (valStringer) String() string { return "v" }

// ptrStringer has a pointer-receiver String(). Only *ptrStringer implements fmt.Stringer; the value type doesn't.
type ptrStringer struct{}

func (*ptrStringer) String() string { return "p" }

// namedStr returns a named string type, so it doesn't satisfy fmt.Stringer, and fmt won't auto-invoke it.
type myString string

type namedStr struct{}

func (namedStr) String() myString { return "n" }

// notAKey has a ToHex method but isn't a keys type and doesn't implement Stringer.
type notAKey struct{}

func (notAKey) ToHex() string { return "" }

// errStringer implements both fmt.Stringer and error. fmt invokes Error() before String() for %s/%v/%q, so a bare value
// formats via Error() and removing .String() would change output.
type errStringer struct{}

func (errStringer) String() string { return "s" }
func (errStringer) Error() string  { return "e" }

// fmtFormatter implements both fmt.Stringer and fmt.Formatter. fmt invokes Format() before String() for every verb, so
// removing .String() would change output.
type fmtFormatter struct{}

func (fmtFormatter) String() string         { return "s" }
func (fmtFormatter) Format(fmt.State, rune) {}

// stringerBytes is a named []byte that implements fmt.Stringer. %x on it encodes String()'s result, not the raw bytes.
type stringerBytes []byte

func (stringerBytes) String() string { return "sb" }

func _() {
	var v valStringer
	var p ptrStringer
	var pp = &p
	var n namedStr

	// Redundant .String() under %s.
	_ = fmt.Errorf("transfer %s has invalid type %s", v.String(), v) // want `unnecessary .String`

	// %v and %q also invoke the Stringer.
	_ = fmt.Sprintf("%v", v.String()) // want `unnecessary .String`
	_ = fmt.Sprintf("%q", v.String()) // want `unnecessary .String`

	// ToHex on keys.Private/Public is redundant: those types implement Stringer with String()==ToHex().
	_ = fmt.Sprintf("%q", keys.GeneratePrivateKey().ToHex())          // want `unnecessary .ToHex`
	_ = fmt.Sprintf("%s", keys.GeneratePrivateKey().Public().ToHex()) // want `unnecessary .ToHex`

	// ToHex on a non-key type is not covered: it's not auto-invoked by fmt. No report.
	_ = fmt.Sprintf("%s", notAKey{}.ToHex())

	// Pointer receiver: *T implements Stringer, so the call on a pointer is redundant.
	_ = fmt.Sprintf("%s", pp.String()) // want `unnecessary .String`

	// Pointer receiver on a value: T does NOT implement fmt.Stringer, so removing .String() would change behavior. No report.
	_ = fmt.Sprintf("%s", p.String())

	// Named-string return type does not satisfy fmt.Stringer. No report.
	_ = fmt.Sprintf("%s", n.String())

	// Non-Stringer verbs: .String() is not redundant. No report.
	_ = fmt.Sprintf("%T", v.String())

	// %#v uses GoStringer, not Stringer, and prints the Go-syntax representation, so the two forms differ. No report.
	_ = fmt.Sprintf("%#v", v.String())

	// Correct usage without .String(): no report.
	_ = fmt.Sprintf("%s", v)

	// Width/precision indirection makes positional mapping unreliable: skipped.
	_ = fmt.Sprintf("%*s", 4, v.String())

	// Verb count correctly maps the second %s to the second argument.
	_ = fmt.Sprintf("%d and %s", 1, v.String()) // want `unnecessary .String`

	// fmt calls Error()/Format() ahead of String(), so on these types .String() is not redundant. No report.
	_ = fmt.Sprintf("%s", errStringer{}.String())
	_ = fmt.Sprintf("%s", fmtFormatter{}.String())

	b := []byte{0xde, 0xad}

	// hex.EncodeToString under %s / %v is replaceable by %x on the raw bytes.
	_ = fmt.Errorf("refund tx mismatch, got: %s", hex.EncodeToString(b)) // want `unnecessary hex.EncodeToString`
	_ = fmt.Sprintf("%v", hex.EncodeToString(b))                         // want `unnecessary hex.EncodeToString`

	// Escaped %% before the verb must not shift the rewritten offset, and doesn't consume an argument.
	_ = fmt.Sprintf("100%% done %s", hex.EncodeToString(b)) // want `unnecessary hex.EncodeToString`

	// A preceding verb means the hex arg is the second variadic element: both argIndex and offset must be right.
	_ = fmt.Errorf("%d bytes: %s", 2, hex.EncodeToString(b)) // want `unnecessary hex.EncodeToString`

	// Raw string literal: offsets map directly with no escape decoding.
	_ = fmt.Sprintf(`raw: %v`, hex.EncodeToString(b)) // want `unnecessary hex.EncodeToString`

	// Escape sequence shifts byte offsets relative to the decoded string, so the diagnostic fires but no fix is offered.
	_ = fmt.Errorf("line\n%s", hex.EncodeToString(b)) // want `unnecessary hex.EncodeToString`

	// %x already takes raw bytes, so there's nothing to flag here.
	_ = fmt.Sprintf("%x", b)

	// %q quotes the result, so %x is not equivalent. No report.
	_ = fmt.Sprintf("%q", hex.EncodeToString(b))

	// %#v prints a quoted Go literal, so %x is not equivalent. No report.
	_ = fmt.Sprintf("%#v", hex.EncodeToString(b))

	// Precision counts input bytes for %x but output runes for %s/%v, so %.3x != %.3s. No report.
	_ = fmt.Sprintf("%.3s", hex.EncodeToString(b))
	_ = fmt.Sprintf("%.3v", hex.EncodeToString(b))

	// The space flag separates hex bytes (% x) but is a no-op for %s. No report.
	_ = fmt.Sprintf("% s", hex.EncodeToString(b))

	// The sharp flag adds a "0x" prefix for %x but does nothing for %s. No report.
	_ = fmt.Sprintf("%#s", hex.EncodeToString(b))

	// %x on a Stringer-implementing []byte encodes String()'s result, not the raw bytes, so it isn't equivalent. No report.
	var sb stringerBytes
	_ = fmt.Sprintf("%s", hex.EncodeToString(sb))
}
