package tree

import (
	"testing"

	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarkExitingNodesMarksDescendantsWatchtowerExited covers the whole point of
// the status: once a node's direct tx confirms, the raw txid its children spend can
// never confirm, so their exit path is gone rather than merely at risk.
func TestMarkExitingNodesMarksDescendantsWatchtowerExited(t *testing.T) {
	f := newExitingTree(t)
	c := newRenewalChain(t, f)

	f.confirm(t, 1_000, c.s1DirectTx)

	assert.Equal(t, st.TreeNodeStatusOnChain, f.reload(t, c.s1).Status,
		"the confirmed node records its own confirmation and is not swept")
	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, c.s2).Status,
		"the walk starts at the children, not below them")
	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, c.s3).Status)
	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, c.leaf).Status,
		"the leaf must be blocked from further transfers")
}

// TestMarkExitingNodesWatchtowerSweepReachesBelowBranchChild is the case the raw
// cascade cannot reach: its gate asks what the watchtower will broadcast next,
// which is moot once the parent's direct tx has already confirmed.
func TestMarkExitingNodesWatchtowerSweepReachesBelowBranchChild(t *testing.T) {
	f := newExitingTree(t)

	s1, s1RawTx, s1DirectTx := f.newRenewalRoot(t)

	branchRaw, branchTx := f.spendSeq(t, s1RawTx.TxHash(), 0, branchSequence)
	branch := f.newNodeTxs(t, s1, st.TreeNodeStatusSplitted, branchRaw, nil)

	leafRaw, _ := f.spendSeq(t, branchTx.TxHash(), 0, renewalRawSequence)
	leaf := f.newNodeTxs(t, branch, st.TreeNodeStatusAvailable, leafRaw, nil)
	require.NoError(t, ent.DbCommit(f.ctx))

	f.confirm(t, 1_000, s1DirectTx)

	assert.Equal(t, st.TreeNodeStatusSplitted, f.reload(t, branch).Status,
		"a branch is already terminal, so the sweep passes through without rewriting it")
	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, leaf).Status,
		"the leaf below that branch has no exit path left")
}

// TestMarkExitingNodesWatchtowerSweepUpgradesParentExited covers the one status
// this pass may overwrite that the raw cascade may not, and confirms the rest of
// the exclusions hold: terminal does not mean free to clobber chain state.
func TestMarkExitingNodesWatchtowerSweepUpgradesParentExited(t *testing.T) {
	f := newExitingTree(t)

	s1, s1RawTx, s1DirectTx := f.newRenewalRoot(t)

	upgraded := []st.TreeNodeStatus{
		st.TreeNodeStatusParentExited,
		st.TreeNodeStatusAvailable,
		st.TreeNodeStatusSplitLocked,
	}
	preserved := []st.TreeNodeStatus{
		st.TreeNodeStatusOnChain,
		st.TreeNodeStatusExited,
		st.TreeNodeStatusSplitted,
		st.TreeNodeStatusReimbursed,
		st.TreeNodeStatusConsolidated,
	}

	children := make(map[st.TreeNodeStatus]*ent.TreeNode)
	for i, status := range append(append([]st.TreeNodeStatus{}, upgraded...), preserved...) {
		raw, _ := f.spendSeq(t, s1RawTx.TxHash(), uint32(i), renewalRawSequence)
		children[status] = f.newNodeTxs(t, s1, status, raw, nil)
	}
	require.NoError(t, ent.DbCommit(f.ctx))

	f.confirm(t, 1_000, s1DirectTx)

	for _, status := range upgraded {
		assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, children[status]).Status,
			"a child in %s must be marked", status)
	}
	for _, status := range preserved {
		assert.Equal(t, status, f.reload(t, children[status]).Status,
			"a child in %s must keep its status", status)
	}
}

// TestMarkExitingNodesWatchtowerSweepWinsInTheSameBlock pins the pass order: a
// branch and a renewal node below it can confirm in one block, and writing
// PARENT_EXITED last would hand a dead subtree back to the watchtower.
func TestMarkExitingNodesWatchtowerSweepWinsInTheSameBlock(t *testing.T) {
	f := newExitingTree(t)
	c := newRenewalChain(t, f)

	f.confirm(t, 1_000, c.branchTx, c.s1DirectTx)

	assert.Equal(t, st.TreeNodeStatusOnChain, f.reload(t, c.s1).Status)
	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, c.s2).Status,
		"the raw cascade also covers S2, but its status must not be the one that lands")
	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, c.leaf).Status)
}

// TestMarkExitingNodesRawConfirmationDoesNotMarkWatchtowerExited keeps the two
// passes distinct: a confirmed raw tx makes the child's direct tx publishable
// without publishing it, so the subtree is at risk, not beyond recovery.
func TestMarkExitingNodesRawConfirmationDoesNotMarkWatchtowerExited(t *testing.T) {
	f := newExitingTree(t)
	c := newRenewalChain(t, f)

	f.confirm(t, 1_000, c.branchTx)

	for _, node := range []*ent.TreeNode{c.s1, c.s2, c.s3, c.leaf} {
		assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, node).Status)
	}
}

// TestMarkExitingNodesWatchtowerSweepRootsOnNullRawTxid pins the nullable arm of
// the root query: raw_txid is Optional, so matching roots on a bare NOT IN
// evaluates to NULL for such a row and drops the subtree it is meant to keep.
//
// Reaching that shape takes a deliberate clear, since the create hook derives
// raw_txid from raw_tx and raw_tx is NotEmpty. An update that leaves raw_tx alone
// does not re-derive it, which is also how a row predating the column keeps a
// null raw_txid after its direct_tx is written.
func TestMarkExitingNodesWatchtowerSweepRootsOnNullRawTxid(t *testing.T) {
	f := newExitingTree(t)
	c := newRenewalChain(t, f)

	_, err := f.tc.Client.TreeNode.UpdateOneID(c.s1.ID).ClearRawTxid().Save(t.Context())
	require.NoError(t, err)

	f.confirm(t, 1_000, c.s1DirectTx)

	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, c.s2).Status,
		"a root with no raw_txid must still anchor the sweep")
	assert.Equal(t, st.TreeNodeStatusWatchtowerExited, f.reload(t, c.leaf).Status)
}

// TestMarkExitingNodesWatchtowerSweepStopsAtTheDepthCap checks the watchtower
// sweep shares the cycle guard, and costs one level of reach by rooting a level
// higher.
func TestMarkExitingNodesWatchtowerSweepStopsAtTheDepthCap(t *testing.T) {
	f := newExitingTree(t)
	_, rootDirectTx, descendants := newLinearRenewalChain(t, f, maxExitSweepDepth+1)

	f.confirm(t, 1_000, rootDirectTx)

	assert.Equal(t, st.TreeNodeStatusWatchtowerExited,
		f.reload(t, descendants[maxExitSweepDepth-1]).Status,
		"everything up to the cap must still be marked")
	assert.Equal(t, st.TreeNodeStatusAvailable,
		f.reload(t, descendants[maxExitSweepDepth]).Status,
		"the level past the cap is left transferable with no exit path")
	assert.Equal(t, 1, f.unmarkedDescendants(t, st.TreeNodeStatusWatchtowerExited),
		"exactly the levels past the cap are left behind")
}
