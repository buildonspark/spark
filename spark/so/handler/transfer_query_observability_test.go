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
