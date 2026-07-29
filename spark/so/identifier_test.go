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
		index              uint32
		expectedIdentifier string
	}{
		{index: 0, expectedIdentifier: strings.Repeat("0", 63) + "1"},
		{index: 1, expectedIdentifier: strings.Repeat("0", 63) + "2"},
		{index: 2, expectedIdentifier: strings.Repeat("0", 63) + "3"},
		{index: 15, expectedIdentifier: strings.Repeat("0", 62) + "10"},
		{index: 255, expectedIdentifier: strings.Repeat("0", 61) + "100"},
		{index: 256, expectedIdentifier: strings.Repeat("0", 61) + "101"},
		{index: 1023, expectedIdentifier: strings.Repeat("0", 61) + "400"},
		{index: 65535, expectedIdentifier: strings.Repeat("0", 59) + "10000"},
		{index: math.MaxUint32 - 1, expectedIdentifier: strings.Repeat("0", 56) + "ffffffff"},
		{index: math.MaxUint32, expectedIdentifier: strings.Repeat("0", 55) + "100000000"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("Index_%d", tt.index),
			func(t *testing.T) {
				identifier := IndexToIdentifier(tt.index)
				require.Equal(t, tt.expectedIdentifier, identifier)
			},
		)
	}
}
