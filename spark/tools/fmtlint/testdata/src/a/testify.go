package a

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeT satisfies both assert.TestingT and require.TestingT.
type fakeT struct{}

func (fakeT) Errorf(string, ...interface{}) {}
func (fakeT) FailNow()                      {}

func _testify(t *testing.T, b *testing.B, tb testing.TB) {
	var ft fakeT
	var v valStringer // value-receiver Stringer, declared in a.go
	bytes := []byte{0xde, 0xad}

	// Redundant .String() / hex.EncodeToString are flagged inside a testify formatted message, in the "f" variant...
	require.Equalf(ft, 1, 2, "got %s", v.String())              // want `unnecessary .String`
	assert.Zerof(ft, 0, "hex is %s", hex.EncodeToString(bytes)) // want `unnecessary hex.EncodeToString`

	// ...and in the msgAndArgs form, where the format string is the first trailing argument.
	assert.Equal(ft, 1, 2, "got %s", v.String()) // want `unnecessary .String`

	// The *Assertions method form is handled the same way.
	a := assert.New(ft)
	a.Equalf(1, 2, "got %s", v.String()) // want `unnecessary .String`

	// Fail/Failf carry a leading failureMessage before the format string, so the format-string index differs from the
	// other assertions; both forms still locate the real format string and flag the redundant .String().
	assert.Failf(ft, "failmsg", "got %s", v.String()) // want `unnecessary .String`
	assert.Fail(ft, "failmsg", "got %s", v.String())  // want `unnecessary .String`

	// No report: a bare message (no trailing args) is used verbatim by testify, so its verbs never invoke the Stringer.
	assert.Equal(ft, 1, 2, "literal %s here")
	require.Equal(ft, 1, 2, "just a message")

	// No report: argument-indexed width makes verb mapping unreliable.
	require.Equalf(ft, 1, 2, "%*s", 4, v.String())

	t.Errorf("got %s", v.String())  // want `unnecessary .String`
	b.Errorf("got %s", v.String())  // want `unnecessary .String`
	tb.Errorf("got %s", v.String()) // want `unnecessary .String`
}
