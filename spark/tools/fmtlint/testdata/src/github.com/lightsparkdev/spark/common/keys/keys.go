// Package keys is a minimal stand-in for github.com/lightsparkdev/spark/common/keys.
//
// The fmtlint analysistest loads testdata in GOPATH mode, which can't reach the real module, so this stub mirrors just
// the ToHex/String surface that the linter and the testdata exercise. The import path must match the real package,
// since the linter identifies key types by their fully-qualified type name.
package keys

type Public struct{}

func (Public) ToHex() string  { return "" }
func (Public) String() string { return Public{}.ToHex() }

type Private struct{}

func (Private) ToHex() string  { return "" }
func (Private) String() string { return Private{}.ToHex() }
func (Private) Public() Public { return Public{} }

func GeneratePrivateKey() Private { return Private{} }
