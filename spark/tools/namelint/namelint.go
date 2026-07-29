// Package namelint reports identifiers named for their side of a comparison rather than for the value they hold.
//
// `got` and `want` are a table-test convention. They say which side of an assertion a value sits on and nothing
// about what it is, so every comparison sends the reader back to the declaration to find out:
//
//	if got := len(req.GetSenderPackages()); got > MaxSenderPackages {       // got what?
//	if packages := len(req.GetSenderPackages()); packages > MaxSenderPackages {
//
// Naming the value keeps the comparison self-describing, and the expectation side reads the same way with an
// `expected` prefix: `numItems != expectedNumItems`.
//
// Diagnostics land on the declaration, the only place a rename is needed. The bare names and their suffixed forms
// (gotHex, wantErr, want_seq) are covered; words that merely begin with the same letters (gotten, wanted) are not.
package namelint

import (
	"fmt"
	"go/ast"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"
)

func init() {
	register.Plugin("namelint", New)
}

// Settings holds plugin configuration. It's required by golangci-lint's API.
type Settings struct{}

type Plugin struct{}

func New(settings any) (register.LinterPlugin, error) {
	if _, err := register.DecodeSettings[Settings](settings); err != nil {
		return nil, err
	}
	return &Plugin{}, nil
}

func (p *Plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{
		{
			Name: "namelint",
			Doc:  "reports identifiers named got/want instead of for the value they hold",
			Run:  run,
		},
	}, nil
}

func (p *Plugin) GetLoadMode() string {
	// Defs separates a declaration from the uses that follow it, so each name is reported once.
	return register.LoadModeTypesInfo
}

var comparisonSideNames = []string{"got", "want"}

func namedForComparisonSide(name string) bool {
	for _, prefix := range comparisonSideNames {
		suffix, isPrefixed := strings.CutPrefix(name, prefix)
		if !isPrefixed {
			continue
		}
		if suffix == "" {
			return true
		}
		// A suffix starting a new word (gotHex, want_seq, got2) is the same convention with a noun bolted on.
		// A lowercase letter means the prefix was just the start of an ordinary word (gotten, wanted).
		// Decoded as a rune, not a byte: identifiers may be non-ASCII, and the lead byte of a lowercase
		// multi-byte rune lands in the uppercase Latin-1 range (é -> 0xC3 -> Ã), which would misfire.
		first, _ := utf8.DecodeRuneInString(suffix)
		if first == '_' || unicode.IsUpper(first) || unicode.IsDigit(first) {
			return true
		}
	}
	return false
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			ident, isIdent := node.(*ast.Ident)
			if !isIdent {
				return true
			}
			if pass.TypesInfo.Defs[ident] == nil || !namedForComparisonSide(ident.Name) {
				return true
			}
			pass.Report(analysis.Diagnostic{
				Pos: ident.Pos(),
				Message: fmt.Sprintf(
					"%q names the side of a comparison rather than the value; name it for what it holds, e.g. numItems / expectedNumItems",
					ident.Name,
				),
			})
			return true
		})
	}
	return nil, nil
}
