package so

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndexToIdentifier(t *testing.T) {
	tests := []struct {
		index uint32
		want  string
	}{
		{index: 0, want: strings.Repeat("0", 63) + "1"},
		{index: 1, want: strings.Repeat("0", 63) + "2"},
		{index: 2, want: strings.Repeat("0", 63) + "3"},
		{index: 15, want: strings.Repeat("0", 62) + "10"},
		{index: 255, want: strings.Repeat("0", 61) + "100"},
		{index: 256, want: strings.Repeat("0", 61) + "101"},
		{index: 1023, want: strings.Repeat("0", 61) + "400"},
		{index: 65535, want: strings.Repeat("0", 59) + "10000"},
		{index: math.MaxUint32 - 1, want: strings.Repeat("0", 56) + "ffffffff"},
		{index: math.MaxUint32, want: strings.Repeat("0", 55) + "100000000"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("Index_%d", tt.index),
			func(t *testing.T) {
				identifier := IndexToIdentifier(tt.index)
				require.Equal(t, tt.want, identifier)
			},
		)
	}
}
