package tree

import (
	"context"
	"encoding/hex"
	"log/slog"

	pb "github.com/lightsparkdev/spark-go/proto/spark_tree"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/tree"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
)

// DenominationMaxPow is the maximum power of 2 for leaf denominations.
const DenominationMaxPow = 30

// DenominationMax is the maximum allowed denomination value for a leaf, calculated as 2^DenominationMaxPow.
const DenominationMax = uint64(1) << DenominationMaxPow

// SmallDenominationsMaxPow is the maximum power of 2 for small denominations.
const SmallDenominationsMaxPow = 13

// DefaultDenominationsCounts is the target number of leaves of each denomination to maintain.
var DefaultDenominationsCounts = map[uint64]uint64{
	1:             5_000,
	2:             5_000,
	4:             5_000,
	8:             5_000,
	16:            2_500,
	32:            2_500,
	64:            2_500,
	128:           2_500,
	256:           1_000,
	512:           1_000,
	1024:          1_000,
	2048:          1_000,
	4096:          500,
	8192:          500,
	16_384:        500,
	32_768:        500,
	65_536:        500,
	131_072:       100,
	262_144:       100,
	524_288:       100,
	1_048_576:     100,
	2_097_152:     100,
	4_194_304:     0,
	8_388_608:     0,
	16_777_216:    0,
	33_554_432:    0,
	67_108_864:    0,
	134_217_728:   0,
	268_435_456:   0,
	536_870_912:   0,
	1_073_741_824: 0,
}

// GetLeafDenominationCounts returns the counts of each leaf denomination for a given owner.
func GetLeafDenominationCounts(ctx context.Context, req *pb.GetLeafDenominationCountsRequest) (*pb.GetLeafDenominationCountsResponse, error) {
	logger := helper.GetLoggerFromContext(ctx)

	network := schema.Network(req.Network)
	err := network.UnmarshalProto(req.Network)
	if err != nil {
		return nil, err
	}

	db := ent.GetDbFromContext(ctx)
	leaves, err := db.TreeNode.Query().
		Where(treenode.OwnerIdentityPubkey(req.OwnerIdentityPublicKey)).
		Where(treenode.StatusEQ(schema.TreeNodeStatusAvailable)).
		Where(
			treenode.HasTreeWith(
				tree.NetworkEQ(network),
			),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[uint64]uint64)
	for _, leaf := range leaves {
		// Leaves must be a power of 2 and less than or equal to the maximum denomination.
		if leaf.Value&(leaf.Value-1) != 0 || leaf.Value > DenominationMax || leaf.Value == 0 {
			logger.Info("invalid leaf denomination", slog.Uint64("denomination", leaf.Value),
				slog.Bool("not_power_of_2", leaf.Value&(leaf.Value-1) != 0),
				slog.Bool("exceeds_max", leaf.Value > DenominationMax),
				slog.Bool("is_zero", leaf.Value == 0))
			continue
		}
		counts[leaf.Value]++
	}
	logger.Info("leaf count", slog.Int("num_leaves", len(leaves)), slog.String("public_key", hex.EncodeToString(req.OwnerIdentityPublicKey)))
	return &pb.GetLeafDenominationCountsResponse{Counts: counts}, nil
}

// ProposeTreeDenominations is called with the amount of sats we have available, the number of users we expect to need to support, and
// returns the list of denominations we should use for the tree. The SSP is responsible for taking this and mapping it to a structure.
func ProposeTreeDenominations(ctx context.Context, req *pb.ProposeTreeDenominationsRequest) (*pb.ProposeTreeDenominationsResponse, error) {
	logger := helper.GetLoggerFromContext(ctx)

	// Figure out how many leaves of each denomination we are missing.
	leafDenominationCounts, err := GetLeafDenominationCounts(ctx, &pb.GetLeafDenominationCountsRequest{
		OwnerIdentityPublicKey: req.SspIdentityPublicKey,
		Network:                req.Network,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("leaf denomination counts", slog.Any("counts", leafDenominationCounts.Counts), slog.String("public_key", hex.EncodeToString(req.SspIdentityPublicKey)))

	minTreeDepth := req.MinTreeDepth
	if minTreeDepth == 0 {
		minTreeDepth = 6
	}

	maxTreeDepth := req.MaxTreeDepth
	if maxTreeDepth == 0 {
		maxTreeDepth = 12
	}

	return solveLeafDenominations(ctx, leafDenominationCounts, DefaultDenominationsCounts, req.MaxAmountSats, minTreeDepth, maxTreeDepth)
}
