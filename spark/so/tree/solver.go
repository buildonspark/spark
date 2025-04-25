package tree

import (
	"context"
	"log/slog"
	"math/bits"

	pb "github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so/logging"
)

// chunkIntoPowersOf2 splits a list of numbers into chunks of size at most maxChunkSize,
// where each chunk's length is	a power of 2. both minChunkSize and maxChunkSize are
// expected to be powers of 2.
func chunkIntoPowersOf2(nums []uint64, minChunkSize int, maxChunkSize int) [][]uint64 {
	chunks := [][]uint64{}
	currentChunk := []uint64{}
	for _, num := range nums {
		if len(currentChunk) >= maxChunkSize {
			chunks = append(chunks, currentChunk)
			currentChunk = []uint64{}
		}
		currentChunk = append(currentChunk, num)
	}
	if len(currentChunk) >= minChunkSize && len(currentChunk) > 0 {
		// For the last chunk, we need to truncate it to a power of 2.
		targetLength := uint64(1) << (bits.Len64(uint64(len(currentChunk))) - 1)
		currentChunk = currentChunk[:targetLength]
		chunks = append(chunks, currentChunk)
	}
	return chunks
}

func solveLeafDenominations(ctx context.Context, counts *pb.GetLeafDenominationCountsResponse, targetCounts map[uint64]uint64, maxAmountSats uint64, minTreeDepth uint64, maxTreeDepth uint64) (*pb.ProposeTreeDenominationsResponse, error) {
	logger := logging.GetLoggerFromContext(ctx).With("method", "tree.solveLeafDenominations")

	// Figure out how many leaves of each denomination we are missing.
	missingCount := make([]uint64, DenominationMaxPow)
	for i := 0; i < DenominationMaxPow; i++ {
		currentDenomination := uint64(1) << i
		if counts.Counts[currentDenomination] <= targetCounts[currentDenomination] {
			missingCount[i] = targetCounts[currentDenomination] - counts.Counts[currentDenomination]
			if missingCount[i] > 0 {
				logger.Info("missing denomination", slog.Uint64("denomination", currentDenomination), slog.Uint64("missing", missingCount[i]))
			}
		}
	}

	// Use Langrange multipliers to minimize (count-target)^2 subject to sum(value) <= max_amount_sats.
	numerator := float64(0)
	denominator := float64(0)
	for i := 0; i < DenominationMaxPow; i++ {
		currentDenomination := uint64(1) << i
		denominator += float64(currentDenomination) * float64(currentDenomination)
		numerator += float64(missingCount[i]) * float64(currentDenomination)
	}
	numerator -= float64(maxAmountSats)
	targetCount := make([]uint64, DenominationMaxPow)
	for i := 0; i < DenominationMaxPow; i++ {
		currentDenomination := uint64(1) << i
		targetCount[i] = missingCount[i] - uint64(float64(currentDenomination)*numerator/denominator)
	}

	// Get the list of denominations we need to propose.
	remainingSats := maxAmountSats
	smallDenominations := []uint64{}
	largeDenominations := []uint64{}
	for i := 0; i < DenominationMaxPow; i++ {
		currentDenomination := uint64(1) << i
		for j := uint64(0); j < targetCount[i]; j++ {
			if remainingSats < currentDenomination {
				break
			}
			if i <= SmallDenominationsMaxPow {
				smallDenominations = append(smallDenominations, currentDenomination)
			} else {
				largeDenominations = append(largeDenominations, currentDenomination)
			}
			remainingSats -= currentDenomination
		}
	}

	// Split it up into a list of trees, each of which has depth up to maxTreeDepth.
	trees := []*pb.ProposeTree{}
	minNumLeaves := int(1) << minTreeDepth
	maxNumLeaves := int(1) << maxTreeDepth

	// Truncate the leaves to a power of 2 if applicable.
	if len(smallDenominations) > 0 {
		chunks := chunkIntoPowersOf2(smallDenominations, minNumLeaves, maxNumLeaves)
		for _, chunk := range chunks {
			trees = append(trees, &pb.ProposeTree{
				IsSmall: true,
				Leaves:  chunk,
			})
			logger.Info("proposed tree", slog.Int("leaves", len(chunk)), slog.Uint64("min", chunk[0]), slog.Uint64("max", chunk[len(chunk)-1]))
		}
	}

	if len(largeDenominations) > 0 {
		chunks := chunkIntoPowersOf2(largeDenominations, minNumLeaves, maxNumLeaves)
		for _, chunk := range chunks {
			trees = append(trees, &pb.ProposeTree{
				IsSmall: false,
				Leaves:  chunk,
			})
			logger.Info("proposed tree", slog.Int("leaves", len(chunk)), slog.Uint64("min", chunk[0]), slog.Uint64("max", chunk[len(chunk)-1]))
		}
	}

	return &pb.ProposeTreeDenominationsResponse{
		Trees: trees,
	}, nil
}
