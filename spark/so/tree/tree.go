package tree

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var (
	meter = otel.Meter("spark.so.tree")

	// exitSweepDepthCapCounter is what alerts when the descendant walk stops
	// early; nothing alerts on log level, so the Errorf beside it is for
	// diagnosis after the counter fires, not the notification itself.
	exitSweepDepthCapCounter metric.Int64Counter
)

func init() {
	var err error
	exitSweepDepthCapCounter, err = meter.Int64Counter(
		"spark_tree_exit_sweep_depth_cap_hit_total",
		metric.WithDescription("Times an exit-sweep descendant walk stopped at maxExitSweepDepth, leaving descendants unmarked"),
	)
	if err != nil {
		otel.Handle(err)
		exitSweepDepthCapCounter = noop.Int64Counter{}
	}
}

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
	// Either spend path confirming puts the node itself on chain; which one it was
	// only decides how far below the node the marking reaches.
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
		if err := cascadeExitingRenewalNodes(ctx, dbClient, confirmedTxids, blockHeight); err != nil {
			return err
		}

		// A raw tx and a descendant's direct tx can confirm in the same block, so the
		// two passes overlap. Order is not load bearing, though not by symmetry: the
		// pass above will not overwrite WATCHTOWER_EXITED, while this one upgrades
		// PARENT_EXITED (see ShouldMarkWatchtowerExited). Preserved one way and
		// replaced the other, the terminal status lands either way.
		if err := cascadeWatchtowerExitedNodes(ctx, dbClient, confirmedTxids, blockHeight); err != nil {
			return err
		}
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

// maxExitSweepDepth bounds the descendant walk so a parent cycle cannot stall
// block processing. Not a renewal-depth limit: each extend-leaf appends a split
// node and nothing prunes, so it must stay far enough ahead of real chains that
// reaching it means a cycle rather than a busy leaf.
const maxExitSweepDepth = 1024

// willWatchtowerBroadcastDirectTx mirrors checkAndBroadcastNodeTx's preconditions
// (watchtower.go:376-395) so "the watchtower will publish this" stays one test
// rather than two kept in sync. Not a status test: branches are what it excludes,
// and an older unscoped sweep left many of them in PARENT_EXITED, not SPLITTED.
func willWatchtowerBroadcastDirectTx(node *ent.TreeNode) bool {
	if len(node.DirectTx) == 0 {
		return false
	}
	directTx, err := common.TxFromRawTxBytes(node.DirectTx)
	if err != nil || len(directTx.TxIn) != 1 {
		return false
	}
	return bitcointransaction.HasBlockRelativeTimelock(directTx.TxIn[0].Sequence)
}

// cascadeExitingRenewalNodes marks the children of every node that is exiting to
// L1, then everything below each of those children whose direct_tx the watchtower
// can now broadcast (watchtower.go:409). That broadcast conflict-spends the
// outpoint the child's raw_tx names, and every descendant names that raw_txid, so
// the whole chain below loses its exit path at once.
//
// direct_txid is deliberately not one of the columns matched. A confirmed
// direct_tx kills its children's raw_tx outright, a stronger claim than this pass
// makes, and it reaches branch children the broadcast gate here rejects;
// cascadeWatchtowerExitedNodes owns that case. Matching them here too would write
// a status that pass immediately replaces, for no change in the final result.
func cascadeExitingRenewalNodes(ctx context.Context, dbClient *ent.Client, confirmedTxids []st.TxID, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)

	hasExitingParent := treenode.HasParentWith(treenode.Or(
		treenode.RawTxidIn(confirmedTxids...),
		treenode.RawRefundTxidIn(confirmedTxids...),
		treenode.DirectRefundTxidIn(confirmedTxids...),
		treenode.DirectFromCpfpRefundTxidIn(confirmedTxids...),
	))

	// Only children that could otherwise still reach AVAILABLE are marked; see
	// ShouldMarkParentExited for the per-status reasoning. Branches in particular
	// must keep SPLITTED: a parent confirming advances their exit rather than
	// invalidating it, and PARENT_EXITED would return them to the watchtower to
	// retry a timelock-disabled tx forever.
	countChildren, err := dbClient.TreeNode.Update().
		Where(hasExitingParent, treenode.StatusNotIn(st.ParentExitSweepExcludedStatuses()...)).
		SetStatus(st.TreeNodeStatusParentExited).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark children of exiting nodes: %w", err)
	}
	logger.Sugar().Infof("Marked %d children of exiting nodes as %v at block height %d",
		countChildren, st.TreeNodeStatusParentExited, blockHeight)

	children, err := dbClient.TreeNode.Query().
		Where(hasExitingParent).
		Select(treenode.FieldID, treenode.FieldDirectTx).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query children of exiting nodes: %w", err)
	}

	roots := make([]uuid.UUID, 0, len(children))
	for _, child := range children {
		if willWatchtowerBroadcastDirectTx(child) {
			roots = append(roots, child.ID)
		}
	}
	if len(roots) == 0 {
		return nil
	}

	count, err := markDescendants(ctx, dbClient, roots, st.TreeNodeStatusParentExited, st.ParentExitSweepExcludedStatuses())
	if err != nil {
		return fmt.Errorf("failed to mark descendants of sweepable nodes: %w", err)
	}
	logger.Sugar().Infof("Marked %d descendants of %d sweepable child nodes %+q as %v at block height %d",
		count, len(roots), roots, st.TreeNodeStatusParentExited, blockHeight)
	return nil
}

// cascadeWatchtowerExitedNodes marks every descendant of a node whose direct_tx
// just confirmed. That confirmation spends the outpoint the node's raw_tx names,
// and both of a child's spend paths hang off that one output, so nothing about a
// child's own transactions can rescue it. Hence no gate, and rooted at the
// confirmed node so the walk covers its children too.
func cascadeWatchtowerExitedNodes(ctx context.Context, dbClient *ent.Client, confirmedTxids []st.TxID, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)

	// The raw_txid arms are load bearing rather than defensive: the column is
	// nullable, so a bare NOT IN evaluates to NULL and drops the rows it should keep.
	roots, err := dbClient.TreeNode.Query().
		Where(
			treenode.DirectTxidIn(confirmedTxids...),
			treenode.Or(
				treenode.RawTxidIsNil(),
				treenode.RawTxidNotIn(confirmedTxids...),
			),
		).
		IDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to query nodes with confirmed direct txs: %w", err)
	}
	if len(roots) == 0 {
		return nil
	}

	count, err := markDescendants(ctx, dbClient, roots, st.TreeNodeStatusWatchtowerExited, st.WatchtowerExitSweepExcludedStatuses())
	if err != nil {
		return fmt.Errorf("failed to mark descendants of watchtower-exited nodes: %w", err)
	}
	logger.Sugar().Infof("Marked %d descendants of %d watchtower-exited nodes %+q as %v at block height %d",
		count, len(roots), roots, st.TreeNodeStatusWatchtowerExited, blockHeight)
	return nil
}

// markDescendants marks every descendant of roots, excluding the given statuses,
// and returns the number of rows updated. The roots themselves are left alone.
// Traversal is structural rather than status-driven: it descends *through* nodes
// it must not rewrite, because the marking no longer cascades one hop per block
// and a skipped node would otherwise cut the walk short.
func markDescendants(ctx context.Context, dbClient *ent.Client, roots []uuid.UUID, status st.TreeNodeStatus, excluded []st.TreeNodeStatus) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)

	childIDsOf := func(parents []uuid.UUID) ([]uuid.UUID, error) {
		return dbClient.TreeNode.Query().
			Where(treenode.HasParentWith(treenode.IDIn(parents...))).
			IDs(ctx)
	}

	currentLevelIDs, err := childIDsOf(roots)
	if err != nil {
		return 0, fmt.Errorf("failed to query children of exiting nodes: %w", err)
	}

	// currentLevelIDs is always the level not yet marked, so the post-loop check
	// reflects what was left behind.
	total := 0
	for depth := 0; len(currentLevelIDs) > 0 && depth < maxExitSweepDepth; depth++ {
		count, err := dbClient.TreeNode.Update().
			Where(
				treenode.IDIn(currentLevelIDs...),
				treenode.StatusNotIn(excluded...),
			).
			SetStatus(status).
			Save(ctx)
		if err != nil {
			return total, fmt.Errorf("failed to mark descendants at depth %d: %w", depth, err)
		}
		total += count
		currentLevelIDs, err = childIDsOf(currentLevelIDs)
		if err != nil {
			return total, fmt.Errorf("failed to query descendants at depth %d: %w", depth, err)
		}
	}
	if len(currentLevelIDs) > 0 {
		// The counter is what alerts; returning rather than erroring keeps every
		// unrelated tree in the same block from failing with it.
		exitSweepDepthCapCounter.Add(ctx, 1)
		logger.Sugar().Errorf("Exit sweep for %v hit the %d-depth cap with %d nodes still unvisited below %+q; descendants beyond that depth remain transferable with no exit path",
			status, maxExitSweepDepth, len(currentLevelIDs), roots)
	}
	return total, nil
}

// Checks whether a tree node status can transition to TreeNodeStatusAvailable.
func TreeNodeCanBecomeAvailable(node *ent.TreeNode) bool {
	return node.Status.CanBecomeAvailable()
}
