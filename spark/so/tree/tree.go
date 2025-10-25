package tree

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
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

// Marks exiting nodes and their children with a proper status and confirmation height in batch update query to the DB.
// It takes a list of confirmed in a bitcoin block transaction id hashes and sends it to Postgres to update the tree nodes that have those txids.
func MarkExitingNodes(ctx context.Context, dbTx *ent.Tx, confirmedTxHashSet map[[32]byte]bool, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)

	confirmedTxids := make([][]byte, 0, len(confirmedTxHashSet))
	for txid := range confirmedTxHashSet {
		confirmedTxids = append(confirmedTxids, txid[:])
	}

	// The state goes from OnChain to Exited, so we need to mark the nodes as OnChain first.
	countOnChain, err := dbTx.TreeNode.Update().SetStatus(st.TreeNodeStatusOnChain).
		SetNodeConfirmationHeight(uint64(blockHeight)).
		Where(treenode.Or(
			treenode.RawTxidIn(confirmedTxids...),
			treenode.DirectTxidIn(confirmedTxids...),
		)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark exiting nodes as on chain: %w", err)
	}
	logger.Sugar().Infof("MarkExitingNodes: marked %d nodes as %v at block height %d", countOnChain, st.TreeNodeStatusOnChain, blockHeight)

	countExited, err := dbTx.TreeNode.Update().SetStatus(st.TreeNodeStatusExited).
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
	logger.Sugar().Infof("MarkExitingNodes: marked %d nodes as %v at block height %d", countExited, st.TreeNodeStatusExited, blockHeight)

	// With 2 counters, we may update the child node twice (first when we see the
	// node tx on chain, and then again when the node is exited). We can potentially
	// optimize this by marking the children only when the parent is marked as OnChain,
	// but it is safer to do it for each status.
	if countOnChain > 0 || countExited > 0 {
		exitedTreeNodes, err := dbTx.TreeNode.Query().Where(treenode.Or(
			treenode.RawTxidIn(confirmedTxids...),
			treenode.DirectTxidIn(confirmedTxids...),
			treenode.RawRefundTxidIn(confirmedTxids...),
			treenode.DirectRefundTxidIn(confirmedTxids...),
			treenode.DirectFromCpfpRefundTxidIn(confirmedTxids...),
		)).
			Select(treenode.FieldID).
			All(ctx)
		if err != nil {
			return err
		}

		var exitedTreeNodesIds []uuid.UUID
		for _, treeNode := range exitedTreeNodes {
			exitedTreeNodesIds = append(exitedTreeNodesIds, treeNode.ID)
		}
		countParentExited, err := dbTx.TreeNode.Update().
			Where(treenode.HasParentWith(treenode.IDIn(exitedTreeNodesIds...))).
			SetStatus(st.TreeNodeStatusParentExited).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update child nodes status: %w", err)
		}
		logger.Sugar().Infof("Child tree nodes %+q marked as unusable because %d parent nodes are exiting at block height %d",
			exitedTreeNodesIds,
			countParentExited,
			blockHeight,
		)
	}

	// Query TreeNode IDs that have TransferLeaf entities with confirmed intermediate refund TXIDs
	transferLeafNodeIDs, err := dbTx.TransferLeaf.Query().
		Where(transferleaf.Or(
			transferleaf.IntermediateRefundTxidIn(confirmedTxids...),
			transferleaf.IntermediateDirectRefundTxidIn(confirmedTxids...),
			transferleaf.IntermediateDirectFromCpfpRefundTxidIn(confirmedTxids...),
		)).
		QueryLeaf().
		IDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to query tree node IDs from transfer leaves with confirmed txids: %w", err)
	}

	// Batch update TreeNodes to OnChain status based on confirmed TransferLeaf transactions
	if len(transferLeafNodeIDs) > 0 {
		count, err := dbTx.TreeNode.Update().
			SetStatus(st.TreeNodeStatusOnChain).
			SetRefundConfirmationHeight(uint64(blockHeight)).
			Where(treenode.IDIn(transferLeafNodeIDs...)).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to mark tree nodes as on chain from transfer leaves: %w", err)
		}
		logger.Sugar().Infof("MarkExitingNodes: marked %d tree nodes as %v from transfer leaves at block height %d",
			count, st.TreeNodeStatusOnChain, blockHeight)
	}

	return nil
}

// Checks whether a tree node status can transition to TreeNodeStatusAvailable.
func TreeNodeCanBecomeAvailable(node *ent.TreeNode) bool {
	switch node.Status {
	case st.TreeNodeStatusSplitted:
		return false
	case st.TreeNodeStatusOnChain:
		return false
	case st.TreeNodeStatusExited:
		return false
	case st.TreeNodeStatusParentExited:
		return false
	case st.TreeNodeStatusReimbursed:
		return false
	default:
		return true
	}
}
