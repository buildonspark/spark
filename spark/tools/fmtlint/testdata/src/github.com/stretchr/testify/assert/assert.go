// Package assert is a minimal stand-in for github.com/stretchr/testify/assert.
//
// The fmtlint analysistest loads testdata in GOPATH mode, which can't reach the real module, so this stub mirrors just
// the assertion surface the linter and testdata exercise. The import path and the msgAndArgs/msg-string signature
// shapes must match the real package, since the linter identifies testify calls by their package path and the position
// of the format string within the signature.
package assert

type TestingT interface {
	Errorf(format string, args ...interface{})
}

func Equal(t TestingT, expected, actual interface{}, msgAndArgs ...interface{}) bool { return true }
func Equalf(t TestingT, expected, actual interface{}, msg string, args ...interface{}) bool {
	return true
}

func Zero(t TestingT, i interface{}, msgAndArgs ...interface{}) bool        { return true }
func Zerof(t TestingT, i interface{}, msg string, args ...interface{}) bool { return true }

// Fail/Failf carry a leading failureMessage before their message/format argument, so the format string sits at a
// different index than in the other assertions — exercising the linter's format-string location logic.
func Fail(t TestingT, failureMessage string, msgAndArgs ...interface{}) bool { return true }
func Failf(t TestingT, failureMessage, msg string, args ...interface{}) bool { return true }

type Assertions struct{ t TestingT }

func New(t TestingT) *Assertions { return &Assertions{t: t} }

func (a *Assertions) Equal(expected, actual interface{}, msgAndArgs ...interface{}) bool { return true }
func (a *Assertions) Equalf(expected, actual interface{}, msg string, args ...interface{}) bool {
	return true
}
