package tree

import (
	"testing"

	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
	"github.com/stretchr/testify/assert"
)

func TestSolveLeafDenominations(t *testing.T) {
	tests := []struct {
		name          string
		currentCounts map[uint64]uint64
		targetCounts  map[uint64]uint64
		maxAmountSats uint64
		maxTreeDepth  uint64
		expectError   bool
		expectedSmall []uint64
		expectedLarge []uint64
	}{
		{
			name:          "basic test with empty current counts",
			currentCounts: map[uint64]uint64{},
			targetCounts: map[uint64]uint64{
				1: 2,
				2: 2,
				4: 2,
				8: 2,
			},
			maxAmountSats: 100,
			maxTreeDepth:  15,
			expectError:   false,
			expectedSmall: []uint64{1, 1, 2, 2, 4, 4, 8, 8},
			expectedLarge: []uint64{},
		},
		{
			name: "test with existing counts",
			currentCounts: map[uint64]uint64{
				1: 1,
				2: 1,
				4: 1,
				8: 1,
			},
			targetCounts: map[uint64]uint64{
				1: 2,
				2: 2,
				4: 2,
				8: 2,
			},
			maxAmountSats: 15,
			maxTreeDepth:  15,
			expectError:   false,
			expectedSmall: []uint64{1, 2, 4, 8},
			expectedLarge: []uint64{},
		},
		{
			name:          "test with large denominations",
			currentCounts: map[uint64]uint64{},
			targetCounts: map[uint64]uint64{
				16384: 2,
				32768: 2,
			},
			maxAmountSats: 98304,
			maxTreeDepth:  15,
			expectError:   false,
			expectedSmall: []uint64{},
			expectedLarge: []uint64{16384, 16384, 32768, 32768},
		},
		{
			name: "test with no new denominations needed",
			currentCounts: map[uint64]uint64{
				1: 1,
				2: 1,
				4: 1,
				8: 1,
			},
			targetCounts: map[uint64]uint64{
				1: 1,
				2: 1,
				4: 1,
				8: 1,
			},
			maxAmountSats: 15000,
			maxTreeDepth:  15,
			expectError:   false,
			expectedSmall: []uint64{},
			expectedLarge: []uint64{},
		},
		{
			name: "test with insufficient max amount sats",
			currentCounts: map[uint64]uint64{
				1: 2,
			},
			targetCounts: map[uint64]uint64{
				1: 2,
				2: 2,
				4: 2,
				8: 2,
			},
			maxAmountSats: 1,
			maxTreeDepth:  15,
			expectError:   false,
			expectedSmall: []uint64{},
			expectedLarge: []uint64{},
		},
		{
			name:          "basic test with binding tree depth",
			currentCounts: map[uint64]uint64{},
			targetCounts: map[uint64]uint64{
				1: 2,
				2: 2,
				4: 2,
				8: 2,
			},
			maxAmountSats: 100,
			maxTreeDepth:  2,
			expectError:   false,
			expectedSmall: []uint64{1, 1, 2, 2},
			expectedLarge: []uint64{},
		},
		{
			name:          "test prioritizing small denominations",
			currentCounts: map[uint64]uint64{},
			targetCounts: map[uint64]uint64{
				1:  2,
				2:  2,
				4:  2,
				8:  2,
				16: 2,
				32: 2,
				64: 2,
			},
			maxAmountSats: 10000,
			maxTreeDepth:  2,
			expectError:   false,
			expectedSmall: []uint64{1, 1, 2, 2},
			expectedLarge: []uint64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := solveLeafDenominations(
				&pb.GetLeafDenominationCountsResponse{Counts: tt.currentCounts},
				tt.targetCounts,
				tt.maxAmountSats,
				tt.maxTreeDepth,
			)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedSmall, result.SmallDenominations)
			assert.Equal(t, tt.expectedLarge, result.LargeDenominations)
		})
	}
}
