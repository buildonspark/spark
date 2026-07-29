package namelint

import (
	"testing"

	"github.com/golangci/plugin-module-register/register"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"
)

func analyzer(t *testing.T) *analysis.Analyzer {
	t.Helper()
	p, err := New(nil)
	require.NoError(t, err)
	analyzers, err := p.BuildAnalyzers()
	require.NoError(t, err)
	require.Len(t, analyzers, 1)
	return analyzers[0]
}

func TestNamelint(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer(t), "a")
}

// analysistest populates TypesInfo itself whatever GetLoadMode returns, so nothing above would
// notice the syntax-only mode. Under golangci-lint that mode leaves TypesInfo unpopulated, the
// Defs lookup never matches, and the whole check silently passes everything.
func TestGetLoadModeRequiresTypesInfo(t *testing.T) {
	p, err := New(nil)
	require.NoError(t, err)
	require.Equal(t, register.LoadModeTypesInfo, p.GetLoadMode())
}

func TestNamedForComparisonSide(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		expected   bool
	}{
		{name: "bare got", identifier: "got", expected: true},
		{name: "bare want", identifier: "want", expected: true},
		{name: "camel suffix", identifier: "gotHex", expected: true},
		{name: "camel suffix on want", identifier: "wantErr", expected: true},
		{name: "snake suffix", identifier: "want_seq", expected: true},
		{name: "digit suffix", identifier: "got2", expected: true},
		{name: "ordinary word got", identifier: "gotten", expected: false},
		{name: "ordinary word want", identifier: "wanted", expected: false},
		{name: "lowercase non-ASCII continuation", identifier: "goté", expected: false},
		{name: "lowercase non-ASCII continuation on want", identifier: "wanté", expected: false},
		{name: "lowercase umlaut continuation", identifier: "gotö", expected: false},
		{name: "uppercase non-ASCII continuation", identifier: "gotÉ", expected: true},
		{name: "unrelated prefix match", identifier: "gotham", expected: false},
		{name: "descriptive name", identifier: "numItems", expected: false},
		{name: "expected prefix is fine", identifier: "expectedNumItems", expected: false},
		{name: "contains but does not start with", identifier: "forgotten", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, namedForComparisonSide(tt.identifier))
		})
	}
}
