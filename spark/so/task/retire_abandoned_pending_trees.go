package task

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/ent/utxo"
)

const (
	// Every healthy tree observed since the occupancy cohort cutoff activated
	// within 24h of creation (96% under 1h), so three days of an all-CREATING
	// PENDING tree means the creation flow died between CreateTree and
	// broadcast and nothing will ever advance it.
	retireAbandonedTreeMinAge = 3 * 24 * time.Hour
	// Bounds a single run so a backlog degrades to steady hourly progress
	// instead of one ever-growing run.
	retireAbandonedTreesPerRun = 10
)

// retireAbandonedPendingTrees retires trees whose creation flow was abandoned
// mid-protocol: the tree never left PENDING, every node is still CREATING, and
// the funding transaction was never seen on-chain. Such trees are minted in
// bulk by from-utxo CreateTree flows whose client dies between CreateTree and
// broadcast, and nothing else drains them — the chain watcher only advances a
// tree whose funding tx confirms.
//
// Only trees with at least one parented node are eligible. A lone CREATING
// root is a deposit whose owner may still broadcast the funding tx weeks
// later; retiring it would leave that late-confirming deposit unclaimable.
// Lone roots are bounded to one row per deposit address, so leaving them is
// cheap.
//
// Each tree is retired in its own transaction (the session begins a fresh one
// after every in-task commit), so progress committed before a timeout or a
// per-tree failure stands.
func retireAbandonedPendingTrees(ctx context.Context) error {
	logger := logging.GetLoggerFromContext(ctx)
	cutoff := time.Now().Add(-retireAbandonedTreeMinAge)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	candidateIDs, err := db.Tree.Query().
		Where(
			tree.StatusEQ(st.TreeStatusPending),
			tree.HasNodesWith(treenode.HasParent()),
			tree.Not(tree.HasNodesWith(treenode.StatusNEQ(st.TreeNodeStatusCreating))),
			tree.Not(tree.HasNodesWith(treenode.CreateTimeGT(cutoff))),
			// Trees the funding-evidence check would skip forever (no deposit
			// address, or one that recorded a confirmation) must not become
			// candidates: selection is oldest-first under a cap, so permanent
			// skippers at the head would otherwise consume every slot on every
			// run and starve the sweep.
			tree.HasDepositAddressWith(
				depositaddress.Or(
					depositaddress.ConfirmationHeightIsNil(),
					depositaddress.ConfirmationHeightEQ(0),
				),
			),
		).
		Order(ent.Asc(tree.FieldCreateTime)).
		Limit(retireAbandonedTreesPerRun).
		IDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to query abandoned pending tree candidates: %w", err)
	}
	// End the selection transaction so each tree below gets its own.
	if err := ent.DbRollback(ctx); err != nil {
		return fmt.Errorf("failed to end candidate selection transaction: %w", err)
	}
	if len(candidateIDs) == 0 {
		return nil
	}

	var errs []error
	for _, treeID := range candidateIDs {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		nodesRetired, err := retireOneAbandonedTree(ctx, treeID, cutoff)
		if err != nil {
			errs = append(errs, fmt.Errorf("tree %s: %w", treeID, err))
			continue
		}
		if nodesRetired > 0 {
			logger.Sugar().Infof(
				"Retired abandoned pending tree %s (%d nodes) to CREATION_ABANDONED",
				treeID,
				nodesRetired,
			)
		}
	}
	return errors.Join(errs...)
}

// retireOneAbandonedTree re-verifies and retires one tree in its own
// transaction, which it always ends: commit when the tree was retired,
// rollback on a skip or any failure, so the next tree never inherits this
// one's transaction. Returns the number of nodes retired; 0 with a nil error
// means the in-transaction re-check disqualified the tree.
func retireOneAbandonedTree(ctx context.Context, treeID uuid.UUID, cutoff time.Time) (int, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodesRetired, err := retireTreeInTx(ctx, db, treeID, cutoff)
	if err != nil || nodesRetired == 0 {
		if rollbackErr := ent.DbRollback(ctx); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to roll back retire transaction: %w", rollbackErr))
		}
		return 0, err
	}
	if err := ent.DbCommit(ctx); err != nil {
		// A failed commit leaves the transaction attached to the session; roll
		// it back so the next tree starts on a fresh one.
		if rollbackErr := ent.DbRollback(ctx); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to roll back after commit failure: %w", rollbackErr))
		}
		return 0, fmt.Errorf("failed to commit retire transaction: %w", err)
	}
	return nodesRetired, nil
}

// retireTreeInTx re-verifies the candidate under locks on its tree row and its
// deposit-address row, then applies the retirement. The tree-row FOR UPDATE
// serializes against the chain watcher's status flip, and the deposit-address
// FOR UPDATE serializes against the watcher's funding-confirmation writes — so
// evidence committed by a concurrent watcher transaction is visible to the
// re-check below rather than landing invisibly while it runs.
func retireTreeInTx(ctx context.Context, db *ent.Client, treeID uuid.UUID, cutoff time.Time) (int, error) {
	logger := logging.GetLoggerFromContext(ctx)

	lockedTree, err := db.Tree.Query().
		Where(tree.ID(treeID), tree.StatusEQ(st.TreeStatusPending)).
		ForUpdate().
		Only(ctx)
	if ent.IsNotFound(err) {
		// The tree advanced (or was removed) since candidate selection.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to lock tree: %w", err)
	}

	depositAddress, err := lockedTree.QueryDepositAddress().ForUpdate().Only(ctx)
	if ent.IsNotFound(err) {
		// Candidate selection requires a deposit address; it disappearing since
		// then means the tree changed under us — leave it for a later run.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to lock deposit address: %w", err)
	}

	confirmed, err := fundingEverConfirmed(ctx, db, lockedTree, depositAddress)
	if err != nil {
		return 0, err
	}
	if confirmed {
		// The funding tx was seen on-chain, so the tree is stalled rather than
		// dead — the chain watcher (or a re-drive) still owns it.
		logger.Sugar().Warnf(
			"Tree %s matches the abandonment shape but its funding shows confirmation evidence; leaving it alone",
			treeID,
		)
		return 0, nil
	}

	disqualified, err := db.TreeNode.Query().
		Where(
			treenode.HasTreeWith(tree.ID(lockedTree.ID)),
			treenode.Or(
				treenode.StatusNEQ(st.TreeNodeStatusCreating),
				treenode.CreateTimeGT(cutoff),
			),
		).
		Exist(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to re-verify tree nodes: %w", err)
	}
	if disqualified {
		return 0, nil
	}

	nodesRetired, err := db.TreeNode.Update().
		Where(
			treenode.HasTreeWith(tree.ID(lockedTree.ID)),
			treenode.StatusEQ(st.TreeNodeStatusCreating),
		).
		SetStatus(st.TreeNodeStatusCreationAbandoned).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to retire tree nodes: %w", err)
	}

	if err := db.Tree.UpdateOne(lockedTree).
		SetStatus(st.TreeStatusCreationAbandoned).
		Exec(ctx); err != nil {
		return 0, fmt.Errorf("failed to retire tree: %w", err)
	}
	return nodesRetired, nil
}

// fundingEverConfirmed reports whether there is any evidence the tree's
// funding reached the chain: its deposit address recorded a confirmation, or
// the chain watcher stored a UTXO for the tree's base txid.
func fundingEverConfirmed(ctx context.Context, db *ent.Client, lockedTree *ent.Tree, depositAddress *ent.DepositAddress) (bool, error) {
	if depositAddress.ConfirmationHeight != 0 {
		return true, nil
	}

	// The chain watcher stores utxos.txid in display byte order, while
	// base_txid is internal hash order — hence String() and not Bytes().
	displayOrderTxid, err := hex.DecodeString(lockedTree.BaseTxid.String())
	if err != nil {
		return false, fmt.Errorf("failed to decode base txid: %w", err)
	}
	utxoSeen, err := db.Utxo.Query().
		Where(
			utxo.TxidEQ(displayOrderTxid),
			utxo.NetworkEQ(lockedTree.Network),
		).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to check utxos for base txid: %w", err)
	}
	return utxoSeen, nil
}
