package tree

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/treenode"
)

// Marks exiting nodes and their children with a proper status and confirmation height in batch update query to the DB.
// It takes a list of confirmed in a bitcoin block transaction id hashes and sends it to Postgres to update the tree nodes that have those txids.
func MarkExitingNodes(ctx context.Context, dbClient *ent.Client, confirmedTxHashSet map[[32]byte]bool, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)

	confirmedTxids := make([]st.TxID, 0, len(confirmedTxHashSet))
	for txid := range confirmedTxHashSet {
		txidObj, err := st.NewTxIDFromBytes(txid[:])
		if err != nil {
			return fmt.Errorf("failed to convert txid to TxID: %w", err)
		}
		confirmedTxids = append(confirmedTxids, txidObj)
	}

	// The state goes from OnChain to Exited, so we need to mark the nodes as OnChain first.
	countOnChain, err := dbClient.TreeNode.Update().SetStatus(st.TreeNodeStatusOnChain).
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

	countExited, err := dbClient.TreeNode.Update().SetStatus(st.TreeNodeStatusExited).
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
		exitedTreeNodes, err := dbClient.TreeNode.Query().Where(treenode.Or(
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
		// Leaf-aggregation statuses are deliberately left alone. PARENT_EXITED
		// exists to mark a child unusable for transfer or renewal, which both
		// of these already are, so the sweep would only do harm:
		//
		//   CONSOLIDATED — overwriting it destroys the one marker the
		//   watchtower uses to find that node's exit package, and a confirming
		//   parent is precisely when that package becomes broadcastable, so the
		//   owner would lose protection at the moment they need it.
		//
		//   AGGREGATED — retired nodes keep their transaction bytes, and
		//   AGGREGATED is what keeps the watchtower from broadcasting them.
		//   PARENT_EXITED is not terminal, so the sweep would hand those
		//   superseded transactions back to the watchtower to retry forever
		//   against an outpoint the consolidated package already spent.
		//
		//   AGGREGATE_LOCK — a leaf mid-aggregation, whose stored package is
		//   about to be replaced. AGGREGATE_LOCK is the only thing stopping the
		//   watchtower from broadcasting that soon-to-be-stale package, so
		//   replacing it with PARENT_EXITED would publish an old state right as
		//   the exit package is being installed. A broadcast cannot be undone,
		//   whereas the cost of not sweeping is only that a rolled-back leaf
		//   can sit AVAILABLE with an exited parent until the next sweep — so
		//   the irreversible side loses.
		countParentExited, err := dbClient.TreeNode.Update().
			Where(
				treenode.HasParentWith(treenode.IDIn(exitedTreeNodesIds...)),
				treenode.StatusNotIn(
					st.TreeNodeStatusConsolidated,
					st.TreeNodeStatusAggregated,
					st.TreeNodeStatusAggregateLock,
				),
			).
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
	transferLeafNodeIDs, err := dbClient.TransferLeaf.Query().
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
		count, err := dbClient.TreeNode.Update().
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
	return node.Status.CanBecomeAvailable()
}
