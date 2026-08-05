package tree

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Sequences as the SO writes them: a branch node tx disables the relative
// timelock outright, while a renewal split node's direct tx carries
// DirectTimelockOffset, which is what lets the watchtower broadcast it.
// spark.ZeroSequence is a var, so these cannot be constants.
var (
	// The literal branch txs carry in practice — 0xfffffffd, confirmed against
	// live regtest rows. Only bit 31 matters to the cascade, but using the real
	// value keeps the fixture honest about what it is standing in for.
	branchSequence        uint32 = 0xfffffffd
	renewalRawSequence           = spark.ZeroSequence
	renewalDirectSequence        = spark.ZeroSequence | spark.DirectTimelockOffset
)

// spendSeq builds a transaction spending prev with an explicit input sequence.
func (f *exitingTree) spendSeq(t *testing.T, prev chainhash.Hash, index uint32, sequence uint32) ([]byte, *wire.MsgTx) {
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: prev, Index: index},
		Sequence:         sequence,
	})
	tx.AddTxOut(wire.NewTxOut(50_000, f.script))
	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw, tx
}

// newNodeTxs creates a node carrying both a raw (CPFP) and a direct tx, the
// pair MarkExitingNodes reads to decide whether a confirmation cascades.
func (f *exitingTree) newNodeTxs(t *testing.T, parent *ent.TreeNode, status st.TreeNodeStatus, rawTx, directTx []byte) *ent.TreeNode {
	dbClient, err := ent.GetDbFromContext(f.ctx)
	require.NoError(t, err)

	create := dbClient.TreeNode.Create().
		SetTree(f.tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(f.keyshare).
		SetValue(100_000).
		SetVerifyingPubkey(f.ownerKey).
		SetOwnerIdentityPubkey(f.ownerKey).
		SetOwnerSigningPubkey(f.ownerKey).
		SetRawTx(rawTx).
		SetVout(0).
		SetStatus(status)
	if len(directTx) > 0 {
		create.SetDirectTx(directTx)
	}
	if parent != nil {
		create.SetParent(parent)
	}
	node, err := create.Save(f.ctx)
	require.NoError(t, err)
	return node
}

// renewalChain builds the shape a repeatedly-renewed leaf actually has:
// branch -> S1 -> S2 -> S3 -> leaf, where each Sn is a SPLIT_LOCKED renewal
// split node whose direct tx the watchtower can broadcast.
type renewalChain struct {
	s1, s2, s3, leaf     *ent.TreeNode
	branchTx, s1DirectTx *wire.MsgTx
}

func newRenewalChain(t *testing.T, f *exitingTree) *renewalChain {
	branchRaw, branchTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, branchSequence)
	branch := f.newNode(t, nil, st.TreeNodeStatusSplitted, branchRaw)

	// Every node names its parent's *raw* txid in both of its own txs — the
	// asymmetry the bug rests on.
	split := func(parent *ent.TreeNode, parentTx *wire.MsgTx, status st.TreeNodeStatus) (*ent.TreeNode, *wire.MsgTx, *wire.MsgTx) {
		raw, rawTx := f.spendSeq(t, parentTx.TxHash(), 0, renewalRawSequence)
		direct, directTx := f.spendSeq(t, parentTx.TxHash(), 0, renewalDirectSequence)
		return f.newNodeTxs(t, parent, status, raw, direct), rawTx, directTx
	}

	s1, s1Raw, s1Direct := split(branch, branchTx, st.TreeNodeStatusSplitLocked)
	s2, s2Raw, _ := split(s1, s1Raw, st.TreeNodeStatusSplitLocked)
	s3, s3Raw, _ := split(s2, s2Raw, st.TreeNodeStatusSplitLocked)
	leaf, _, _ := split(s3, s3Raw, st.TreeNodeStatusAvailable)

	require.NoError(t, ent.DbCommit(f.ctx))
	return &renewalChain{
		s1: s1, s2: s2, s3: s3, leaf: leaf,
		branchTx: branchTx, s1DirectTx: s1Direct,
	}
}

// TestMarkExitingNodesCascadesBelowRenewalNode covers where the cascade is keyed:
// the branch's own tx confirming is enough, because that makes S1's direct tx
// publishable and its broadcast strands S2, S3 and the leaf.
func TestMarkExitingNodesCascadesBelowRenewalNode(t *testing.T) {
	f := newExitingTree(t)
	c := newRenewalChain(t, f)

	f.confirm(t, 1_000, c.branchTx)

	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, c.s1).Status,
		"the renewal node directly under the exiting branch must be marked")
	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, c.s2).Status,
		"S1's direct tx is now broadcastable, so everything below S1 is stranded")
	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, c.s3).Status,
		"the cascade must not stop at S1's direct child")
	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, c.leaf).Status,
		"the stranded leaf must be blocked from further transfers")

	// S1's direct tx then actually confirms: nothing new to mark, but S1 records it.
	f.confirm(t, 1_052, c.s1DirectTx)

	assert.Equal(t, st.TreeNodeStatusOnChain, f.reload(t, c.s1).Status,
		"the swept renewal node itself records the confirmation")
	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, c.leaf).Status)
}

// TestMarkExitingNodesDoesNotCascadeBelowBranch pins what the gate buys: a branch
// child cannot be swept, so the leaves under it stay spendable. A branch exit can
// span hundreds of them — the repro tree had 991 under its root.
func TestMarkExitingNodesDoesNotCascadeBelowBranch(t *testing.T) {
	f := newExitingTree(t)

	rootRaw, rootTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, branchSequence)
	root := f.newNodeTxs(t, nil, st.TreeNodeStatusSplitted, rootRaw, nil)

	midRaw, midTx := f.spendSeq(t, rootTx.TxHash(), 0, branchSequence)
	mid := f.newNodeTxs(t, root, st.TreeNodeStatusSplitted, midRaw, nil)

	leafRaw, _ := f.spendSeq(t, midTx.TxHash(), 0, renewalRawSequence)
	leafDirect, _ := f.spendSeq(t, midTx.TxHash(), 0, renewalDirectSequence)
	leaf := f.newNodeTxs(t, mid, st.TreeNodeStatusAvailable, leafRaw, leafDirect)
	require.NoError(t, ent.DbCommit(f.ctx))

	f.confirm(t, 1_000, rootTx)

	assert.Equal(t, st.TreeNodeStatusSplitted, f.reload(t, mid).Status,
		"an intermediate branch keeps SPLITTED through an ancestor exit")
	assert.Equal(t, st.TreeNodeStatusAvailable, f.reload(t, leaf).Status,
		"a leaf two hops under an exiting branch must stay spendable")
}

// TestMarkExitingNodesCascadeTraversesSplitted checks the walk is structural:
// it descends through a node it is forbidden to rewrite instead of stopping
// there, which a status-driven cascade could not do.
func TestMarkExitingNodesCascadeTraversesSplitted(t *testing.T) {
	f := newExitingTree(t)

	s1Raw, s1RawTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, renewalRawSequence)
	s1Direct, s1DirectTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, renewalDirectSequence)
	s1 := f.newNodeTxs(t, nil, st.TreeNodeStatusSplitLocked, s1Raw, s1Direct)

	// The SPLITTED node must sit below the cascade root (S2) for the walk to have
	// anything to traverse.
	s2Raw, s2RawTx := f.spendSeq(t, s1RawTx.TxHash(), 0, renewalRawSequence)
	s2Direct, _ := f.spendSeq(t, s1RawTx.TxHash(), 0, renewalDirectSequence)
	s2 := f.newNodeTxs(t, s1, st.TreeNodeStatusSplitLocked, s2Raw, s2Direct)

	midRaw, midTx := f.spendSeq(t, s2RawTx.TxHash(), 0, branchSequence)
	mid := f.newNodeTxs(t, s2, st.TreeNodeStatusSplitted, midRaw, nil)

	leafRaw, _ := f.spendSeq(t, midTx.TxHash(), 0, renewalRawSequence)
	leaf := f.newNodeTxs(t, mid, st.TreeNodeStatusAvailable, leafRaw, nil)
	require.NoError(t, ent.DbCommit(f.ctx))

	f.confirm(t, 1_000, s1DirectTx)

	assert.Equal(t, st.TreeNodeStatusSplitted, f.reload(t, mid).Status,
		"the walk must pass through a SPLITTED node without downgrading it")
	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, leaf).Status,
		"the leaf below that SPLITTED node must still be reached")
}

// TestMarkExitingNodesDoesNotCascadeThroughBranchChild pins the gap SP-3713
// closes: a direct confirmation kills the parent's raw_tx, so a branch child's
// raw_tx is dead too, but the branch fails the gate and nothing cascades.
func TestMarkExitingNodesDoesNotCascadeThroughBranchChild(t *testing.T) {
	f := newExitingTree(t)

	s1Raw, s1RawTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, renewalRawSequence)
	s1Direct, s1DirectTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, renewalDirectSequence)
	s1 := f.newNodeTxs(t, nil, st.TreeNodeStatusSplitLocked, s1Raw, s1Direct)

	branchRaw, branchTx := f.spendSeq(t, s1RawTx.TxHash(), 0, branchSequence)
	branch := f.newNodeTxs(t, s1, st.TreeNodeStatusSplitted, branchRaw, nil)

	leafRaw, _ := f.spendSeq(t, branchTx.TxHash(), 0, renewalRawSequence)
	leaf := f.newNodeTxs(t, branch, st.TreeNodeStatusAvailable, leafRaw, nil)
	require.NoError(t, ent.DbCommit(f.ctx))

	f.confirm(t, 1_000, s1DirectTx)

	assert.Equal(t, st.TreeNodeStatusSplitted, f.reload(t, branch).Status,
		"the branch child keeps SPLITTED")
	assert.Equal(t, st.TreeNodeStatusAvailable, f.reload(t, leaf).Status,
		"known gap: the leaf is stranded but stays spendable until SP-3713")
}

// newLinearRenewalChain builds a root with depth renewal nodes strung below it,
// each carrying the raw/direct pair a real renewal node has, so descendants[i]
// sits at level i+1. The cascade roots on descendants[0], so the walk spends its
// cap over descendants[1:] — which is why maxExitSweepDepth+1 sweeps in full.
func newLinearRenewalChain(t *testing.T, f *exitingTree, depth int) (*wire.MsgTx, []*ent.TreeNode) {
	rootRaw, rootRawTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, renewalRawSequence)
	rootDirect, rootDirectTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, renewalDirectSequence)
	root := f.newNodeTxs(t, nil, st.TreeNodeStatusSplitLocked, rootRaw, rootDirect)

	descendants := make([]*ent.TreeNode, 0, depth)
	parent, parentRawTx := root, rootRawTx
	for i := range depth {
		status := st.TreeNodeStatusSplitLocked
		if i == depth-1 {
			status = st.TreeNodeStatusAvailable
		}
		raw, rawTx := f.spendSeq(t, parentRawTx.TxHash(), 0, renewalRawSequence)
		direct, _ := f.spendSeq(t, parentRawTx.TxHash(), 0, renewalDirectSequence)
		node := f.newNodeTxs(t, parent, status, raw, direct)
		descendants = append(descendants, node)
		parent, parentRawTx = node, rawTx
	}
	require.NoError(t, ent.DbCommit(f.ctx))
	return rootDirectTx, descendants
}

// unmarkedDescendants counts nodes the sweep left behind. The confirmed root
// itself becomes ON_CHAIN, so anything else outside those two statuses is a
// descendant the walk never reached.
func (f *exitingTree) unmarkedDescendants(t *testing.T) int {
	count, err := f.tc.Client.TreeNode.Query().
		Where(treenode.StatusNotIn(st.TreeNodeStatusParentExited, st.TreeNodeStatusOnChain)).
		Count(t.Context())
	require.NoError(t, err)
	return count
}

// TestMarkExitingNodesCascadeReachesTheDepthCap pins the inclusive edge: a chain
// whose deepest node sits on the last permitted level is swept in full, leaving
// nothing behind for the cap check to report.
func TestMarkExitingNodesCascadeReachesTheDepthCap(t *testing.T) {
	f := newExitingTree(t)
	rootDirectTx, descendants := newLinearRenewalChain(t, f, maxExitSweepDepth+1)

	f.confirm(t, 1_000, rootDirectTx)

	assert.Equal(t, st.TreeNodeStatusParentExited,
		f.reload(t, descendants[maxExitSweepDepth]).Status,
		"the node on the last permitted level must be marked")
	assert.Zero(t, f.unmarkedDescendants(t),
		"a chain ending exactly at the cap must sweep completely")
}

// TestMarkExitingNodesCascadeStopsPastTheDepthCap pins what the cap costs: one
// level further and that node stays AVAILABLE, spendable with an exit path its
// ancestor's direct tx already destroyed. This is the condition
// spark_tree_exit_sweep_depth_cap_hit_total alerts on.
func TestMarkExitingNodesCascadeStopsPastTheDepthCap(t *testing.T) {
	f := newExitingTree(t)
	rootDirectTx, descendants := newLinearRenewalChain(t, f, maxExitSweepDepth+2)

	f.confirm(t, 1_000, rootDirectTx)

	assert.Equal(t, st.TreeNodeStatusParentExited,
		f.reload(t, descendants[maxExitSweepDepth]).Status,
		"everything up to the cap must still be marked")
	assert.Equal(t, st.TreeNodeStatusAvailable,
		f.reload(t, descendants[maxExitSweepDepth+1]).Status,
		"the level past the cap is left transferable with no exit path")
	assert.Equal(t, 1, f.unmarkedDescendants(t),
		"exactly the levels past the cap are left behind")
}

// TestMarkExitingNodesDoesNotCascadeFromLegacyParentExitedBranch pins why the
// cascade cannot key off PARENT_EXITED. Branch nodes already carry that status
// from the earlier unscoped sweep, so reading it as "renewal node" would mark
// their whole subtrees the first time one confirmed.
func TestMarkExitingNodesDoesNotCascadeFromLegacyParentExitedBranch(t *testing.T) {
	f := newExitingTree(t)

	// A branch left in PARENT_EXITED by an older binary: timelock-disabled txs,
	// so the watchtower can never sweep it and nothing below it is stranded.
	branchRaw, branchTx := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, branchSequence)
	branchDirect, _ := f.spendSeq(t, f.tree.BaseTxid.Hash(), 0, branchSequence)
	branch := f.newNodeTxs(t, nil, st.TreeNodeStatusParentExited, branchRaw, branchDirect)

	midRaw, midTx := f.spendSeq(t, branchTx.TxHash(), 0, branchSequence)
	mid := f.newNodeTxs(t, branch, st.TreeNodeStatusSplitted, midRaw, nil)

	leafRaw, _ := f.spendSeq(t, midTx.TxHash(), 0, renewalRawSequence)
	leaf := f.newNodeTxs(t, mid, st.TreeNodeStatusAvailable, leafRaw, nil)
	require.NoError(t, ent.DbCommit(f.ctx))

	f.confirm(t, 1_000, branchTx)

	assert.Equal(t, st.TreeNodeStatusSplitted, f.reload(t, mid).Status,
		"the branch below must keep SPLITTED")
	assert.Equal(t, st.TreeNodeStatusAvailable, f.reload(t, leaf).Status,
		"a leaf under a legacy PARENT_EXITED branch must stay spendable")
}
