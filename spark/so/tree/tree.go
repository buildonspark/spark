package tree

import (
	"context"

	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
)

// DenominationMaxPow is the maximum power of 2 for leaf denominations.
const DenominationMaxPow = 30

// DenominationMax is the maximum allowed denomination value for a leaf, calculated as 2^DenominationMaxPow.
const DenominationMax = uint64(1) << DenominationMaxPow

// GetLeafDenominationCounts returns the counts of each leaf denomination for a given owner.
func GetLeafDenominationCounts(ctx context.Context, req *pb.GetLeafDenominationCountsRequest) (*pb.GetLeafDenominationCountsResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	network := st.Network(req.Network)
	err := network.UnmarshalProto(req.Network)
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	leaves, err := db.TreeNode.Query().
		Where(treenode.OwnerIdentityPubkey(req.OwnerIdentityPublicKey)).
		Where(treenode.StatusEQ(st.TreeNodeStatusAvailable)).
		Where(treenode.HasTreeWith(tree.NetworkEQ(network))).
		All(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[uint64]uint64)
	for _, leaf := range leaves {
		// Leaves must be a power of 2 and less than or equal to the maximum denomination.
		if leaf.Value&(leaf.Value-1) != 0 || leaf.Value > DenominationMax || leaf.Value == 0 {
			logger.Sugar().Infof("Invalid leaf denomination %d", leaf.Value)
			continue
		}
		counts[leaf.Value]++
	}
	logger.Sugar().Infof("Leaf count (leaves: %d, public key: %x)", len(leaves), req.OwnerIdentityPublicKey)
	return &pb.GetLeafDenominationCountsResponse{Counts: counts}, nil
}
