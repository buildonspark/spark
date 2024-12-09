package tree

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
)

// DenominationMaxPow is the maximum power of 2 for leaf denominations.
const DenominationMaxPow = 30

// DenominationMax is the maximum allowed denomination value for a leaf, calculated as 2^DenominationMaxPow.
const DenominationMax = uint64(1) << DenominationMaxPow

// MakeChange returns a list of denominations that sum up to the given amount.
func MakeChange(amount uint64) []uint64 {
	change := []uint64{}
	remaining := amount
	for i := DenominationMaxPow; i >= 0; i-- {
		denom := uint64(1) << i
		for denom <= remaining {
			change = append(change, denom)
			remaining -= denom
		}
	}
	if remaining != 0 {
		panic("WTF!")
	}
	return change
}

// GetLeafDenominationCounts returns the counts of each leaf denomination for a given owner.
func GetLeafDenominationCounts(ctx context.Context, req *pb.GetLeafDenominationCountsRequest) (*pb.GetLeafDenominationCountsResponse, error) {
	db := common.GetDbFromContext(ctx)
	leaves, err := db.TreeNode.Query().
		Where(treenode.OwnerIdentityPubkey(req.OwnerIdentityPublicKey)).
		Where(treenode.StatusEQ(schema.TreeNodeStatusAvailable)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[uint64]uint64)
	for _, leaf := range leaves {
		// Leaves must be a power of 2 and less than or equal to the maximum denomination.
		if leaf.Value&(leaf.Value-1) != 0 && leaf.Value <= DenominationMax {
			return nil, fmt.Errorf("invalid leaf denomination: %d", leaf.Value)
		}
		counts[leaf.Value]++
	}
	return &pb.GetLeafDenominationCountsResponse{Counts: counts}, nil
}

// FindLeavesToGiveUser is called to figure out which leaves to give to a user when they deposit funds or receive a lightning payment.
func FindLeavesToGiveUser(ctx context.Context, req *pb.FindLeavesToGiveUserRequest) (*pb.FindLeavesToGiveUserResponse, error) {
	db := common.GetDbFromContext(ctx)
	// TODO: Sort on the polarity score as well.
	leaves, err := db.TreeNode.Query().
		Where(treenode.OwnerIdentityPubkey(req.SspIdentityPublicKey)).
		Where(treenode.StatusEQ(schema.TreeNodeStatusAvailable)).
		Order(ent.Desc(treenode.FieldValue)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	remainingSats := req.AmountSats
	leavesToGive := []uuid.UUID{}
	for _, leaf := range leaves {
		if remainingSats >= leaf.Value {
			leavesToGive = append(leavesToGive, leaf.ID)
			remainingSats -= leaf.Value
		}
	}
	if remainingSats != 0 {
		// Due to the construction of the denominations, this should only happen if we're missing some
		// denominations or if we don't have enough balance.
		return nil, fmt.Errorf("unable to find leaves")
	}

	// Convert []uuid.UUID to [][]byte
	leavesBytes := make([][]byte, len(leavesToGive))
	for i, leaf := range leavesToGive {
		leavesBytes[i] = leaf[:]
	}
	return &pb.FindLeavesToGiveUserResponse{Leaves: leavesBytes}, nil
}

// FindLeavesToTakeFromUser is called to obtain a plan for how to enable the user to send the specified amount of sats to the SSP (i.e. for a lightning payment).
func FindLeavesToTakeFromUser(ctx context.Context, req *pb.FindLeavesToTakeFromUserRequest) (*pb.FindLeavesToTakeFromUserResponse, error) {
	db := common.GetDbFromContext(ctx)

	// TODO: Sort on the polarity score as well.
	leaves, err := db.TreeNode.Query().Where(treenode.OwnerIdentityPubkey(req.UserIdentityPublicKey)).
		Where(treenode.StatusEQ(schema.TreeNodeStatusAvailable)).
		Order(ent.Desc(treenode.FieldValue)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// Split into leaves based on whether they should be taken or possibly swapped.
	remainingSats := req.AmountSats
	leavesToTake := []uuid.UUID{}
	leavesToSwap := []*ent.TreeNode{}
	for _, leaf := range leaves {
		if remainingSats >= leaf.Value {
			leavesToTake = append(leavesToTake, leaf.ID)
			remainingSats -= leaf.Value
		} else if remainingSats > 0 {
			leavesToSwap = append(leavesToSwap, leaf)
		}
	}

	// We have exact change, so we can just take the leaves.
	if remainingSats == 0 {
		// Convert []uuid.UUID to [][]byte
		leavesBytes := make([][]byte, len(leavesToTake))
		for i, leaf := range leavesToTake {
			leavesBytes[i] = leaf[:]
		}
		return &pb.FindLeavesToTakeFromUserResponse{LeavesToTake: leavesBytes}, nil
	}
	// Truncate the list of leaves to swap to the amount we actually need.
	amountTotal := uint64(0)
	for i := 0; i < len(leavesToSwap); i++ {
		amountTotal += leavesToSwap[i].Value
		if amountTotal >= remainingSats {
			leavesToSwap = leavesToSwap[:i+1]
			break
		}
	}
	if amountTotal < remainingSats {
		return nil, fmt.Errorf("insufficient balance")
	}

	// Find leaves that can achieve the specific `remainingSats` amount.
	sspLeaves1, err := FindLeavesToGiveUser(ctx, &pb.FindLeavesToGiveUserRequest{
		SspIdentityPublicKey: req.SspIdentityPublicKey,
		AmountSats:           remainingSats,
	})
	if err != nil {
		return nil, err
	}

	// Find leaves that can achieve the total amount minus the specific `remainingSats` amount.
	sspLeaves2, err := FindLeavesToGiveUser(ctx, &pb.FindLeavesToGiveUserRequest{
		SspIdentityPublicKey: req.SspIdentityPublicKey,
		AmountSats:           amountTotal - remainingSats,
	})
	if err != nil {
		return nil, err
	}

	// The user will give the SSP these leaves.
	userLeaves := [][]byte{}
	for _, leaf := range leavesToTake {
		userLeaves = append(userLeaves, leaf[:])
	}
	for _, leaf := range leavesToSwap {
		userLeaves = append(userLeaves, leaf.ID[:])
	}

	// The SSP will give the user these leaves (swap).
	sspLeaves := [][]byte{}
	sspLeaves = append(sspLeaves, sspLeaves1.Leaves...)
	sspLeaves = append(sspLeaves, sspLeaves2.Leaves...)

	// The difference between take and give should be the amount that the user is sending.
	return &pb.FindLeavesToTakeFromUserResponse{
		LeavesToTake: userLeaves,
		LeavesToSwap: sspLeaves,
	}, nil
}

// ProposeTreeDenominations is called with the amount of sats we have available, the number of users we expect to need to support, and
// returns the list of denominations we should use for the tree. The SSP is responsible for taking this and mapping it to a structure.
func ProposeTreeDenominations(ctx context.Context, req *pb.ProposeTreeDenominationsRequest) (*pb.ProposeTreeDenominationsResponse, error) {
	existing, err := GetLeafDenominationCounts(ctx, &pb.GetLeafDenominationCountsRequest{
		OwnerIdentityPublicKey: req.SspIdentityPublicKey,
	})
	if err != nil {
		return nil, err
	}

	denominations := []uint64{}
	remainingSats := req.AmountSats
	for i := 0; i < DenominationMaxPow; i++ {
		if existing.Counts[uint64(1)<<i] >= uint64(req.NumUserLeaves) {
			// We already have enough of this denomination, skip it!
			continue
		}
		for i := 0; i < int(req.NumUserLeaves); i++ {
			if remainingSats >= uint64(1)<<i {
				denominations = append(denominations, uint64(1)<<i)
				remainingSats -= uint64(1) << i
			}
		}
	}

	if remainingSats != 0 {
		denominations = append(denominations, MakeChange(remainingSats)...)
	}
	return &pb.ProposeTreeDenominationsResponse{Denominations: denominations}, nil
}
