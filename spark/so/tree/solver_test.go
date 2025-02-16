package tree

import (
	"log"
	"testing"

	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
	"github.com/stretchr/testify/assert"
)

func TestChunkIntoPowersOf2(t *testing.T) {
	tests := []struct {
		name           string
		nums           []uint64
		minChunkSize   int
		maxChunkSize   int
		expectedChunks [][]uint64
	}{
		{
			name:           "test with empty list",
			nums:           []uint64{},
			minChunkSize:   2,
			maxChunkSize:   16,
			expectedChunks: [][]uint64{},
		},
		{
			name:           "test with single element (invalid)",
			nums:           []uint64{1},
			minChunkSize:   2,
			maxChunkSize:   16,
			expectedChunks: [][]uint64{}, // No chunks because the single element is less than minChunkSize.
		},
		{
			name:           "test with single element (valid)",
			nums:           []uint64{2},
			minChunkSize:   1,
			maxChunkSize:   16,
			expectedChunks: [][]uint64{{2}},
		},
		{
			name:           "test with lots of chunks",
			nums:           []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14},
			minChunkSize:   4,
			maxChunkSize:   4,
			expectedChunks: [][]uint64{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}},
		},
		{
			name:           "test with lots of chunks",
			nums:           []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			minChunkSize:   2,
			maxChunkSize:   4,
			expectedChunks: [][]uint64{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}, {13, 14}},
		},
		{
			name:           "test with lots of chunks",
			nums:           []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
			minChunkSize:   4,
			maxChunkSize:   16,
			expectedChunks: [][]uint64{{1, 2, 3, 4, 5, 6, 7, 8}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := chunkIntoPowersOf2(tt.nums, tt.minChunkSize, tt.maxChunkSize)
			assert.Equal(t, tt.expectedChunks, chunks)
		})
	}
}

func TestSolveLeafDenominations(t *testing.T) {
	tests := []struct {
		name          string
		currentCounts map[uint64]uint64
		targetCounts  map[uint64]uint64
		maxAmountSats uint64
		minTreeDepth  uint64
		maxTreeDepth  uint64
		expectError   bool
		expectedTrees []*pb.ProposeTree
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
			minTreeDepth:  0,
			maxTreeDepth:  15,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{
				{
					IsSmall: true,
					Leaves:  []uint64{1, 1, 2, 2, 4, 4, 8, 8},
				},
			},
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
			minTreeDepth:  0,
			maxTreeDepth:  15,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{
				{
					IsSmall: true,
					Leaves:  []uint64{1, 2, 4, 8},
				},
			},
		},
		{
			name:          "test with large denominations",
			currentCounts: map[uint64]uint64{},
			targetCounts: map[uint64]uint64{
				16384: 2,
				32768: 2,
			},
			maxAmountSats: 98304,
			minTreeDepth:  0,
			maxTreeDepth:  15,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{
				{
					IsSmall: false,
					Leaves:  []uint64{16384, 16384, 32768, 32768},
				},
			},
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
			minTreeDepth:  0,
			maxTreeDepth:  15,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{},
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
			minTreeDepth:  0,
			maxTreeDepth:  15,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{},
		},
		{
			name:          "basic test with binding amount sats",
			currentCounts: map[uint64]uint64{},
			targetCounts: map[uint64]uint64{
				1: 2,
				2: 2,
				4: 2,
				8: 2,
			},
			maxAmountSats: 7,
			minTreeDepth:  0,
			maxTreeDepth:  2,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{
				{
					IsSmall: true,
					Leaves:  []uint64{1, 1, 2, 2},
				},
			},
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
			minTreeDepth:  0,
			maxTreeDepth:  2,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{
				{
					IsSmall: true,
					Leaves:  []uint64{1, 1, 2, 2},
				},
				{
					IsSmall: true,
					Leaves:  []uint64{4, 4, 8, 8},
				},
				{
					IsSmall: true,
					Leaves:  []uint64{16, 16, 32, 32},
				},
				{
					IsSmall: true,
					Leaves:  []uint64{64, 64},
				},
			},
		},
		{
			name:          "test the kitchen sink",
			currentCounts: map[uint64]uint64{},
			targetCounts: map[uint64]uint64{
				1:     4,
				2:     2,
				4:     2,
				8:     2,
				16:    2,
				32:    2,
				64:    2,
				16384: 13,
			},
			maxAmountSats: 10000000,
			minTreeDepth:  2,
			maxTreeDepth:  4,
			expectError:   false,
			expectedTrees: []*pb.ProposeTree{
				{
					IsSmall: true,
					Leaves:  []uint64{1, 1, 1, 1, 2, 2, 4, 4, 8, 8, 16, 16, 32, 32, 64, 64},
				},
				{
					IsSmall: false,
					Leaves:  []uint64{16384, 16384, 16384, 16384, 16384, 16384, 16384, 16384},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := solveLeafDenominations(
				&pb.GetLeafDenominationCountsResponse{Counts: tt.currentCounts},
				tt.targetCounts,
				tt.maxAmountSats,
				tt.minTreeDepth,
				tt.maxTreeDepth,
			)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedTrees, result.Trees)
		})
	}
}

func TestSolveLeafDenominationsReal(t *testing.T) {
	result, err := solveLeafDenominations(
		&pb.GetLeafDenominationCountsResponse{Counts: map[uint64]uint64{}},
		DefaultDenominationsCounts,
		10_000_000,
		6,
		10,
	)

	assert.NoError(t, err)
	assert.Equal(t, 12, len(result.Trees))

	denomToCount := make(map[uint64]uint64)
	for _, tree := range result.Trees {
		for _, denom := range tree.Leaves {
			denomToCount[denom]++
		}
	}
	log.Printf("denomToCount: %v", denomToCount)
	log.Printf("numTrees: %d", len(result.Trees))
}
