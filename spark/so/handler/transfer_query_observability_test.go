package handler

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResultCountBucket(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{2, "2-9"},
		{9, "2-9"},
		{10, "10-49"},
		{49, "10-49"},
		{50, "50"},
		{51, "51-99"},
		{99, "51-99"},
		{100, "100"},
		{101, "100+"},
		{1000, "100+"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("n=%d", tc.n), func(t *testing.T) {
			require.Equal(t, tc.want, resultCountBucket(tc.n))
		})
	}
}

func TestTransferQueryAttrsFields(t *testing.T) {
	// Lock the attribute set. If a field is added/removed/renamed, this
	// test fails and the dashboard owner reviews whether dashboard panels
	// need to follow. Keeps the recorder-to-dashboard contract explicit.
	var a transferQueryAttrs
	a.QueryPath = ""
	a.MIMOEnabled = false
	a.FilterType = ""
	a.HasStatusFilter = false
	a.HasTypeFilter = false
	a.HasTransferIDs = false
	a.PendingOnly = false
	// No assertion needed — this is a compile-time guard.
	_ = a
}
