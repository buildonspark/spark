// Package require is a minimal stand-in for github.com/stretchr/testify/require.
//
// See the sibling assert stub for why this exists. require's functions return nothing (they abort the test), but the
// signature shape the linter cares about matches assert.
package require

type TestingT interface {
	Errorf(format string, args ...interface{})
	FailNow()
}

func Equal(t TestingT, expected, actual interface{}, msgAndArgs ...interface{})        {}
func Equalf(t TestingT, expected, actual interface{}, msg string, args ...interface{}) {}

func Zero(t TestingT, i interface{}, msgAndArgs ...interface{})        {}
func Zerof(t TestingT, i interface{}, msg string, args ...interface{}) {}
