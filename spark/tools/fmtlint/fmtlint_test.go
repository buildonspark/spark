package fmtlint

import (
	"testing"

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

func TestFmtlint(t *testing.T) {
	analysistest.RunWithSuggestedFixes(t, analysistest.TestData(), analyzer(t), "a")
}

func TestParseVerbsReliable(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   []verbArg
	}{
		{
			name:   "simple",
			format: "%s and %d",
			want:   []verbArg{{verb: 's', argIndex: 0, offset: 1}, {verb: 'd', argIndex: 1, offset: 8}},
		},
		{
			name:   "escaped percent",
			format: "100%% done: %v",
			want:   []verbArg{{verb: 'v', argIndex: 0, offset: 13}},
		},
		{
			name:   "flags width precision",
			format: "%+d %-5.2f %#x",
			want:   []verbArg{{verb: 'd', argIndex: 0, offset: 2}, {verb: 'f', argIndex: 1, offset: 9, hasPrecision: true}, {verb: 'x', argIndex: 2, offset: 13, sharpFlag: true}},
		},
		{
			name:   "sharp flag on v",
			format: "%#v and %v",
			want:   []verbArg{{verb: 'v', argIndex: 0, offset: 2, sharpFlag: true}, {verb: 'v', argIndex: 1, offset: 9}},
		},
		{
			name:   "space flag and precision",
			format: "% s and %.4d",
			want:   []verbArg{{verb: 's', argIndex: 0, offset: 2, spaceFlag: true}, {verb: 'd', argIndex: 1, offset: 11, hasPrecision: true}},
		},
		{
			name:   "escaped percent at end",
			format: "done 100%%",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reliable := parseVerbs(tt.format)
			require.True(t, reliable)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseVerbsUnreliable(t *testing.T) {
	tests := []struct {
		name   string
		format string
	}{
		{name: "star width", format: "%*s"},
		{name: "explicit index", format: "%[2]s %[1]d"},
		{name: "trailing percent", format: "done %"},
		{name: "flags then end of string", format: "value %+"},
		{name: "flags then end after real verb", format: "%s then %#"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, reliable := parseVerbs(tt.format)
			require.False(t, reliable)
		})
	}
}
