package tree

import (
	"math/bits"

	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
)

func solveLeafDenominations(counts *pb.GetLeafDenominationCountsResponse, targetCounts map[uint64]uint64, maxAmountSats uint64, maxTreeDepth uint64) (*pb.ProposeTreeDenominationsResponse, error) {
	// Figure out how many leaves of each denomination we are missing.
	missingCount := make([]uint64, DenominationMaxPow)
	for i := 0; i < DenominationMaxPow; i++ {
		currentDenomination := uint64(1) << i
		if counts.Counts[currentDenomination] <= targetCounts[currentDenomination] {
			missingCount[i] = targetCounts[currentDenomination] - counts.Counts[currentDenomination]
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

	// Truncate the leaves to a power of 2 if applicable.
	if len(smallDenominations) > 0 {
		// Compute 2^floor(log2(len(smallDenominations))).
		targetLength := uint64(1) << (bits.Len64(uint64(len(smallDenominations))) - 1)
		if targetLength > uint64(1)<<maxTreeDepth {
			targetLength = uint64(1) << maxTreeDepth
		}
		if targetLength > 0 && targetLength < uint64(len(smallDenominations)) {
			smallDenominations = smallDenominations[:targetLength]
		}
	}
	if len(largeDenominations) > 0 {
		// Compute 2^floor(log2(len(smallDenominations))).
		targetLength := uint64(1) << (bits.Len64(uint64(len(largeDenominations))) - 1)
		if targetLength > uint64(1)<<maxTreeDepth {
			targetLength = uint64(1) << maxTreeDepth
		}
		if targetLength > 0 && targetLength < uint64(len(largeDenominations)) {
			largeDenominations = largeDenominations[:targetLength]
		}
	}

	return &pb.ProposeTreeDenominationsResponse{
		SmallDenominations: smallDenominations,
		LargeDenominations: largeDenominations,
	}, nil
}
