package tree

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
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
	ownerIdentityPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	leaves, err := db.TreeNode.Query().
		Where(treenode.OwnerIdentityPubkey(ownerIdentityPubKey)).
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
	logger.Sugar().Infof("Leaf count (leaves: %d, public key: %x)", len(leaves), ownerIdentityPubKey)
	return &pb.GetLeafDenominationCountsResponse{Counts: counts}, nil
}

// Marks exiting nodes with a proper status and confirmation height in batch update query to the DB.
// It takes a list of confirmed in a bitcoin block txids and sends it to Postgres to update the tree nodes that have those txids.
func MarkExitingNodes(ctx context.Context, dbTx *ent.Tx, confirmedTxHashSet map[[32]byte]bool, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)

	confirmedTxids := make([][]byte, 0, len(confirmedTxHashSet))
	for txid := range confirmedTxHashSet {
		confirmedTxids = append(confirmedTxids, txid[:])
	}

	// The state goes from OnChain to Exited, so we need to mark the nodes as OnChain first.
	count, err := dbTx.TreeNode.Update().SetStatus(st.TreeNodeStatusOnChain).
		SetNodeConfirmationHeight(uint64(blockHeight)).
		Where(treenode.Or(
			treenode.RawTxidIn(confirmedTxids...),
			treenode.DirectTxidIn(confirmedTxids...),
		)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark exiting nodes as on chain: %w", err)
	}
	logger.Sugar().Infof("MarkExitingNodes: marked %d nodes as %v at block height %d", count, st.TreeNodeStatusOnChain, blockHeight)

	count, err = dbTx.TreeNode.Update().SetStatus(st.TreeNodeStatusExited).
		SetRefundConfirmationHeight(uint64(blockHeight)).
		Where(treenode.Or(
			treenode.RawRefundTxidIn(confirmedTxids...),
			treenode.DirectRefundTxidIn(confirmedTxids...),
			treenode.DirectFromCpfpRefundTxidIn(confirmedTxids...),
		)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark exiting nodes as exited: %w", err)
	}
	logger.Sugar().Infof("MarkExitingNodes: marked %d nodes as %v at block height %d", count, st.TreeNodeStatusExited, blockHeight)

	return nil
}
