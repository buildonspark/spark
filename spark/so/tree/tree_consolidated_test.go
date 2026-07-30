package tree

import (
	"testing"

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

// TestMarkExitingNodesPreservesConsolidated pins the interaction between an
// exiting parent and a consolidated child.
//
// A consolidated node's exit package spends its parent's output, so the
// parent's tx confirming is exactly when the watchtower should broadcast it.
// That same event makes MarkExitingNodes sweep the parent's children to
// PARENT_EXITED — which for a consolidated node would erase the only marker
// the watchtower matches on, silently dropping its coverage for an offline
// owner. Ordinary children must still be swept.
func TestMarkExitingNodesPreservesConsolidated(t *testing.T) {
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
	parentTx := wire.NewMsgTx(3)
	parentTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: tree.BaseTxid.Hash(), Index: 0}})
	parentTx.AddTxOut(wire.NewTxOut(100_000, script))
	parentRaw, err := common.SerializeTx(parentTx)
	require.NoError(t, err)

	// Each node needs its own tx: a child sharing the parent's raw tx would
	// match the confirmed txid itself and be re-statused before the sweep.
	childTx := func(vout uint32) []byte {
		tx := wire.NewMsgTx(3)
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: vout}})
		tx.AddTxOut(wire.NewTxOut(50_000, script))
		raw, err := common.SerializeTx(tx)
		require.NoError(t, err)
		return raw
	}

	newNode := func(parent *ent.TreeNode, status st.TreeNodeStatus, rawTx []byte) *ent.TreeNode {
		create := dbClient.TreeNode.Create().
			SetTree(tree).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(keyshare).
			SetValue(100_000).
			SetVerifyingPubkey(ownerKey).
			SetOwnerIdentityPubkey(ownerKey).
			SetOwnerSigningPubkey(ownerKey).
			SetRawTx(rawTx).
			SetVout(0).
			SetStatus(status)
		if parent != nil {
			create.SetParent(parent)
		}
		node, err := create.Save(ctx)
		require.NoError(t, err)
		return node
	}

	parent := newNode(nil, st.TreeNodeStatusSplitted, parentRaw)
	consolidated := newNode(parent, st.TreeNodeStatusConsolidated, childTx(0))
	ordinary := newNode(parent, st.TreeNodeStatusAvailable, childTx(1))
	aggregated := newNode(parent, st.TreeNodeStatusAggregated, childTx(2))
	locked := newNode(parent, st.TreeNodeStatusAggregateLock, childTx(3))
	require.NoError(t, ent.DbCommit(ctx))

	// The parent's node tx confirms.
	confirmed := map[[32]byte]bool{parentTx.TxHash(): true}
	require.NoError(t, MarkExitingNodes(t.Context(), tc.Client, confirmed, 1_000))

	reloadedConsolidated, err := tc.Client.TreeNode.Get(t.Context(), consolidated.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusConsolidated, reloadedConsolidated.Status,
		"a consolidated child must keep its status so the watchtower can still find its exit package")

	// A retired descendant keeps its transaction bytes, and AGGREGATED is what
	// keeps the watchtower from broadcasting them. PARENT_EXITED is not in the
	// watchtower's terminal set, so sweeping it would hand those superseded
	// transactions back to the watchtower to retry against a spent outpoint.
	reloadedAggregated, err := tc.Client.TreeNode.Get(t.Context(), aggregated.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAggregated, reloadedAggregated.Status,
		"a retired child must stay AGGREGATED so the watchtower keeps ignoring it")

	// AGGREGATE_LOCK is the only thing stopping the watchtower from
	// broadcasting a mid-aggregation leaf's soon-to-be-stale package, and a
	// broadcast cannot be undone.
	reloadedLocked, err := tc.Client.TreeNode.Get(t.Context(), locked.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAggregateLock, reloadedLocked.Status,
		"a locked leaf must keep AGGREGATE_LOCK so the watchtower keeps skipping it")

	reloadedOrdinary, err := tc.Client.TreeNode.Get(t.Context(), ordinary.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusParentExited, reloadedOrdinary.Status,
		"ordinary children must still be marked PARENT_EXITED")
}
