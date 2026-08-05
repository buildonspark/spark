package tree

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exitingTree is the minimum row set MarkExitingNodes needs: a tree, a keyshare
// and a way to add nodes wired to a parent, each with its own transaction.
type exitingTree struct {
	ctx context.Context
	tc  *db.TestContext

	tree     *ent.Tree
	script   []byte
	ownerKey keys.Public
	keyshare *ent.SigningKeyshare
}

func newExitingTree(t *testing.T) *exitingTree {
	ctx, tc := db.NewTestSQLiteContext(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerKey := keys.GeneratePrivateKey().Public()
	tree, err := dbClient.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerKey).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := dbClient.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.GeneratePrivateKey()).
		SetPublicShares(map[string]keys.Public{"operator1": ownerKey}).
		SetPublicKey(ownerKey).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	script, err := common.P2TRScriptFromPubKey(ownerKey)
	require.NoError(t, err)

	return &exitingTree{ctx: ctx, tc: tc, tree: tree, script: script, ownerKey: ownerKey, keyshare: keyshare}
}

// spend builds a transaction spending the given outpoint. Every node needs its
// own: one sharing its parent's raw tx would match the confirmed txid itself
// and be re-statused before the child sweep runs.
// See spendSeq for the variant that sets an explicit input sequence.
func (f *exitingTree) spend(t *testing.T, prev chainhash.Hash, index uint32) ([]byte, *wire.MsgTx) {
	return f.spendSeq(t, prev, index, 0)
}

// newNode creates a node with only a raw (CPFP) tx. See newNodeTxs for the
// variant that also attaches a direct tx.
func (f *exitingTree) newNode(t *testing.T, parent *ent.TreeNode, status st.TreeNodeStatus, rawTx []byte) *ent.TreeNode {
	return f.newNodeTxs(t, parent, status, rawTx, nil)
}

// confirm runs MarkExitingNodes as if the given transaction confirmed in a block.
func (f *exitingTree) confirm(t *testing.T, blockHeight int64, tx *wire.MsgTx) {
	require.NoError(t, MarkExitingNodes(t.Context(), f.tc.Client,
		map[[32]byte]bool{tx.TxHash(): true}, blockHeight))
}

func (f *exitingTree) reload(t *testing.T, node *ent.TreeNode) *ent.TreeNode {
	reloaded, err := f.tc.Client.TreeNode.Get(t.Context(), node.ID)
	require.NoError(t, err)
	return reloaded
}

// TestMarkExitingNodesSweepScope checks that the sweep honours
// ShouldMarkParentExited, which is what SP-3711 turns on. The per-status policy
// itself is pinned cheaply in schematype's TestTreeNodeStatusShouldMarkParentExited;
// this covers the query wiring, with one representative child per outcome.
func TestMarkExitingNodesSweepScope(t *testing.T) {
	f := newExitingTree(t)

	parentRaw, parentTx := f.spend(t, f.tree.BaseTxid.Hash(), 0)
	parent := f.newNode(t, nil, st.TreeNodeStatusSplitted, parentRaw)

	children := make(map[st.TreeNodeStatus]*ent.TreeNode)
	for i, status := range []st.TreeNodeStatus{
		// Swept: the statuses SP-3711 names, all of which could otherwise
		// reach AVAILABLE under a parent that is exiting.
		st.TreeNodeStatusAvailable,
		st.TreeNodeStatusTransferLocked,
		st.TreeNodeStatusSplitLocked,
		// Retained: a branch, and confirmed chain state.
		st.TreeNodeStatusSplitted,
		st.TreeNodeStatusOnChain,
	} {
		raw, _ := f.spend(t, parentTx.TxHash(), uint32(i))
		children[status] = f.newNode(t, parent, status, raw)
	}
	require.NoError(t, ent.DbCommit(f.ctx))

	f.confirm(t, 1_000, parentTx)

	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, children[st.TreeNodeStatusAvailable]).Status,
		"an available leaf under an exiting parent must be blocked")
	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, children[st.TreeNodeStatusTransferLocked]).Status,
		"a transfer-locked leaf drains back to AVAILABLE, so it must still be swept")
	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, children[st.TreeNodeStatusSplitLocked]).Status,
		"a renewal split node must stay non-terminal so the watchtower keeps sweeping it")

	assert.Equal(t, st.TreeNodeStatusSplitted, f.reload(t, children[st.TreeNodeStatusSplitted]).Status,
		"a branch must keep SPLITTED: a confirming parent advances its exit rather than invalidating it")
	assert.Equal(t, st.TreeNodeStatusOnChain, f.reload(t, children[st.TreeNodeStatusOnChain]).Status,
		"confirmed chain state must not be downgraded to PARENT_EXITED")
}

// TestMarkExitingNodesPreservesConfirmationHeight guards the ordering bug
// inside MarkExitingNodes: the child sweep runs after the ON_CHAIN update, so a
// branch marked ON_CHAIN by an earlier block used to be downgraded to
// PARENT_EXITED by a later one, losing the recorded confirmation.
func TestMarkExitingNodesPreservesConfirmationHeight(t *testing.T) {
	f := newExitingTree(t)

	grandparentRaw, grandparentTx := f.spend(t, f.tree.BaseTxid.Hash(), 0)
	branchRaw, branchTx := f.spend(t, grandparentTx.TxHash(), 0)
	leafRaw, _ := f.spend(t, branchTx.TxHash(), 0)

	grandparent := f.newNode(t, nil, st.TreeNodeStatusSplitted, grandparentRaw)
	branch := f.newNode(t, grandparent, st.TreeNodeStatusSplitted, branchRaw)
	leaf := f.newNode(t, branch, st.TreeNodeStatusAvailable, leafRaw)
	require.NoError(t, ent.DbCommit(f.ctx))

	// Block 1000: the branch's own tx confirms, so it becomes ON_CHAIN and its
	// leaf is swept, the branch now being mid-exit.
	f.confirm(t, 1_000, branchTx)
	// Block 1001: the grandparent's tx confirms. The branch is a child of the
	// grandparent, so the sweep reaches it — and must leave it alone.
	f.confirm(t, 1_001, grandparentTx)

	reloadedBranch := f.reload(t, branch)
	assert.Equal(t, st.TreeNodeStatusOnChain, reloadedBranch.Status,
		"a confirmed branch must not be downgraded to PARENT_EXITED by a later ancestor confirmation")
	assert.Equal(t, uint64(1_000), reloadedBranch.NodeConfirmationHeight,
		"the recorded confirmation height must survive the sweep")

	assert.Equal(t, st.TreeNodeStatusParentExited, f.reload(t, leaf).Status,
		"a leaf under an exiting branch must still be blocked")
}
