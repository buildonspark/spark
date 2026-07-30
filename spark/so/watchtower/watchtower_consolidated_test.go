package watchtower

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type consolidatedFixture struct {
	parent *ent.TreeNode
	node   *ent.TreeNode
}

// createConsolidatedFixture builds a CONSOLIDATED node under a parent whose
// node tx confirmed at parentHeight (0 = unconfirmed), carrying a watchtower
// refund with the standard DirectTimelockOffset relative timelock.
func createConsolidatedFixture(t *testing.T, ctx context.Context, parentHeight uint64) *consolidatedFixture {
	t.Helper()
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

	parentCreate := dbClient.TreeNode.Create().
		SetTree(tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(keyshare).
		SetValue(100_000).
		SetVerifyingPubkey(ownerKey).
		SetOwnerIdentityPubkey(ownerKey).
		SetOwnerSigningPubkey(ownerKey).
		SetRawTx(parentRaw).
		SetVout(0).
		SetStatus(st.TreeNodeStatusSplitted)
	if parentHeight > 0 {
		parentCreate.SetNodeConfirmationHeight(parentHeight)
	}
	parent, err := parentCreate.Save(ctx)
	require.NoError(t, err)

	watchtowerTx := wire.NewMsgTx(3)
	watchtowerTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         spark.DirectTimelockOffset,
	})
	watchtowerTx.AddTxOut(wire.NewTxOut(common.MaybeApplyFee(100_000), script))
	watchtowerRaw, err := common.SerializeTx(watchtowerTx)
	require.NoError(t, err)

	node, err := dbClient.TreeNode.Create().
		SetTree(tree).
		SetParent(parent).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(keyshare).
		SetValue(100_000).
		SetVerifyingPubkey(ownerKey).
		SetOwnerIdentityPubkey(ownerKey).
		SetOwnerSigningPubkey(ownerKey).
		SetRawTx(parentRaw).
		SetRawRefundTx(watchtowerRaw).
		SetDirectFromCpfpRefundTx(watchtowerRaw).
		SetVout(0).
		SetStatus(st.TreeNodeStatusConsolidated).
		Save(ctx)
	require.NoError(t, err)

	return &consolidatedFixture{parent: parent, node: node}
}

func TestCheckAndBroadcastConsolidatedRefundTx(t *testing.T) {
	const parentHeight = uint64(1_000)

	t.Run("expired timelock broadcasts the watchtower refund", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		f := createConsolidatedFixture(t, ctx, parentHeight)
		mockClient := &mockBitcoinClient{response: txHash}
		err := checkAndBroadcastConsolidatedRefundTx(ctx, mockClient, f.node, int64(parentHeight+uint64(spark.DirectTimelockOffset)), btcnetwork.Regtest)
		require.NoError(t, err)
		require.NotNil(t, mockClient.seenTX)
		assert.Equal(t, spark.DirectTimelockOffset, mockClient.seenTX.TxIn[0].Sequence)
	})

	t.Run("unexpired timelock does not broadcast", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		f := createConsolidatedFixture(t, ctx, parentHeight)
		mockClient := &mockBitcoinClient{response: txHash}
		err := checkAndBroadcastConsolidatedRefundTx(ctx, mockClient, f.node, int64(parentHeight+uint64(spark.DirectTimelockOffset))-1, btcnetwork.Regtest)
		require.NoError(t, err)
		assert.Nil(t, mockClient.seenTX)
	})

	t.Run("unconfirmed parent does not broadcast", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		f := createConsolidatedFixture(t, ctx, 0)
		mockClient := &mockBitcoinClient{response: txHash}
		err := checkAndBroadcastConsolidatedRefundTx(ctx, mockClient, f.node, 1_000_000, btcnetwork.Regtest)
		require.NoError(t, err)
		assert.Nil(t, mockClient.seenTX)
	})

	t.Run("missing watchtower refund errors", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		f := createConsolidatedFixture(t, ctx, parentHeight)
		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		node, err := dbClient.TreeNode.UpdateOne(f.node).ClearDirectFromCpfpRefundTx().Save(ctx)
		require.NoError(t, err)
		err = checkAndBroadcastConsolidatedRefundTx(ctx, &mockBitcoinClient{response: txHash}, node, 1_000_000, btcnetwork.Regtest)
		require.ErrorContains(t, err, "no watchtower refund tx")
	})

	// A consolidated root's funding output is the long-confirmed deposit, so
	// there is no exit-in-progress signal; broadcasting on sight would
	// force-exit resting funds.
	t.Run("consolidated root is never broadcast", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		f := createConsolidatedFixture(t, ctx, parentHeight)
		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		root, err := dbClient.TreeNode.UpdateOne(f.node).ClearParent().Save(ctx)
		require.NoError(t, err)
		mockClient := &mockBitcoinClient{response: txHash}
		err = checkAndBroadcastConsolidatedRefundTx(ctx, mockClient, root, 1_000_000, btcnetwork.Regtest)
		require.NoError(t, err)
		assert.Nil(t, mockClient.seenTX)
	})
}

// TestCheckExpiredTimeLocksRoutesConsolidated pins the dispatch itself: a
// CONSOLIDATED node must reach the consolidated broadcaster rather than the
// ordinary node-tx / refund arms.
func TestCheckExpiredTimeLocksRoutesConsolidated(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	// Give the node a direct tx and an unconfirmed own node tx: without the
	// CONSOLIDATED dispatch this shape would take the node-tx arm instead.
	node, err := dbClient.TreeNode.UpdateOne(f.node).SetDirectTx(f.node.RawTx).Save(ctx)
	require.NoError(t, err)

	mockClient := &mockBitcoinClient{response: txHash}
	err = CheckExpiredTimeLocks(ctx, dbClient, mockClient, node, int64(1_000+uint64(spark.DirectTimelockOffset)), btcnetwork.Regtest)
	require.NoError(t, err)
	require.NotNil(t, mockClient.seenTX, "consolidated node should have been routed to the exit-package broadcaster")
	// The exit package, not the node tx: fee-deducted and timelocked 50.
	assert.Equal(t, spark.DirectTimelockOffset, mockClient.seenTX.TxIn[0].Sequence)
	assert.Equal(t, common.MaybeApplyFee(100_000), mockClient.seenTX.TxOut[0].Value)
}

// TestCheckExpiredTimeLocksSkipsAggregateLock pins that a node locked for
// aggregation is left alone: an internal node keeps a stale pre-split refund
// package, and broadcasting it would publish an old state. The node carries a
// direct tx and an unconfirmed node tx, so the guard has to precede the
// node-tx arm to suppress it.
func TestCheckExpiredTimeLocksSkipsAggregateLock(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	locked, err := dbClient.TreeNode.UpdateOne(f.node).
		SetStatus(st.TreeNodeStatusAggregateLock).
		SetDirectTx(f.node.RawTx).
		Save(ctx)
	require.NoError(t, err)

	mockClient := &mockBitcoinClient{response: txHash}
	require.NoError(t, CheckExpiredTimeLocks(ctx, dbClient, mockClient, locked, 1_000_000, btcnetwork.Regtest))
	assert.Nil(t, mockClient.seenTX, "a node locked for aggregation must not broadcast anything")
}

func TestQueryBroadcastableNodesIncludesConsolidated(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	// The query runs against the raw client, so the fixture must be visible
	// outside the test transaction.
	require.NoError(t, ent.DbCommit(ctx))

	nodes, err := QueryBroadcastableNodes(t.Context(), tc.Client, 2_000, btcnetwork.Regtest)
	require.NoError(t, err)
	ids := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		ids[n.ID.String()] = true
	}
	assert.True(t, ids[f.node.ID.String()], "consolidated node should be broadcastable")
}

// TestCheckExpiredTimeLocksRereadsStaleStatus pins the re-read: the candidate
// list is built once per block, so a node can be locked for aggregation
// between the query and the broadcast. Acting on the stale snapshot would
// publish a pre-aggregation transaction that conflicts with the exit package
// the flow is installing.
func TestCheckExpiredTimeLocksRereadsStaleStatus(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// A plain available leaf under the confirmed parent, with a
	// broadcastable direct tx and an expired timelock.
	leaf, err := dbClient.TreeNode.Create().
		SetTree(f.node.QueryTree().OnlyX(ctx)).
		SetParent(f.parent).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(f.node.QuerySigningKeyshare().OnlyX(ctx)).
		SetValue(100_000).
		SetVerifyingPubkey(f.node.VerifyingPubkey).
		SetOwnerIdentityPubkey(f.node.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(f.node.OwnerSigningPubkey).
		SetRawTx(f.node.RawTx).
		SetDirectTx(f.node.DirectFromCpfpRefundTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	// The watchtower's snapshot still says AVAILABLE; meanwhile an
	// aggregation locks the row.
	stale := leaf
	_, err = dbClient.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAggregateLock).Save(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusAvailable, stale.Status, "the in-memory snapshot must still look broadcastable")

	mockClient := &mockBitcoinClient{response: txHash}
	require.NoError(t, CheckExpiredTimeLocks(ctx, dbClient, mockClient, stale, 1_000_000, btcnetwork.Regtest))
	assert.Nil(t, mockClient.seenTX, "a node locked after the query must not be broadcast from a stale snapshot")
}

// TestCheckExpiredTimeLocksConsolidatedAlreadyExited covers the sibling branch
// of the CONSOLIDATED dispatch: once the exit package has confirmed there is
// nothing left to broadcast.
func TestCheckExpiredTimeLocksConsolidatedAlreadyExited(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	confirmed, err := dbClient.TreeNode.UpdateOne(f.node).SetRefundConfirmationHeight(1_050).Save(ctx)
	require.NoError(t, err)

	mockClient := &mockBitcoinClient{response: txHash}
	require.NoError(t, CheckExpiredTimeLocks(ctx, dbClient, mockClient, confirmed, 1_000_000, btcnetwork.Regtest))
	assert.Nil(t, mockClient.seenTX, "a consolidated node whose exit package already confirmed must not broadcast again")
}

// TestCheckExpiredTimeLocksRereadsRetiredStatus covers the terminal-status arm
// of the re-read: an aggregation commit retires descendants to AGGREGATED, and
// a node that reaches that status after the candidate query must not have its
// now-superseded transactions broadcast.
func TestCheckExpiredTimeLocksRereadsRetiredStatus(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	leaf, err := dbClient.TreeNode.Create().
		SetTree(f.node.QueryTree().OnlyX(ctx)).
		SetParent(f.parent).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(f.node.QuerySigningKeyshare().OnlyX(ctx)).
		SetValue(100_000).
		SetVerifyingPubkey(f.node.VerifyingPubkey).
		SetOwnerIdentityPubkey(f.node.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(f.node.OwnerSigningPubkey).
		SetRawTx(f.node.RawTx).
		SetDirectTx(f.node.DirectFromCpfpRefundTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	// The snapshot still says AVAILABLE; the aggregation has since committed
	// and retired this leaf.
	stale := leaf
	_, err = dbClient.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAggregated).Save(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusAvailable, stale.Status)

	mockClient := &mockBitcoinClient{response: txHash}
	require.NoError(t, CheckExpiredTimeLocks(ctx, dbClient, mockClient, stale, 1_000_000, btcnetwork.Regtest))
	assert.Nil(t, mockClient.seenTX, "a leaf retired by an aggregation commit must not be broadcast from a stale snapshot")
}

// TestQueryBroadcastableNodesExcludesConsolidatedRoot pins the exclusion at
// the query level, not just in the downstream check: a consolidated root must
// never even be offered as a broadcast candidate, because its funding output
// (the deposit) is confirmed from the moment the tree exists and so carries no
// exit-in-progress signal.
// TestCheckExpiredTimeLocksNeverBroadcastsConsolidatedRoot pins the root
// protection at the layer that actually enforces it.
//
// A consolidated root is a legitimate broadcast candidate — it reaches the loop
// through the ordinary refund-pending query — so the query layer cannot be what
// protects it. What protects it is the funding height: a root's package is
// funded by the deposit, which is confirmed from birth, so there is no
// exit-in-progress signal and broadcasting on sight would force-exit resting
// funds. The owner holds the zero-timelock refund and exits when they choose.
func TestCheckExpiredTimeLocksNeverBroadcastsConsolidatedRoot(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	// A root with its own node tx confirmed: no parent, but a real candidate.
	root, err := dbClient.TreeNode.UpdateOne(f.node).
		ClearParent().
		SetNodeConfirmationHeight(1_000).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, ent.DbCommit(ctx))

	// Guard against a vacuous test: the root must really reach the loop.
	nodes, err := QueryBroadcastableNodes(t.Context(), tc.Client, 2_000, btcnetwork.Regtest)
	require.NoError(t, err)
	found := false
	for _, n := range nodes {
		if n.ID == root.ID {
			found = true
		}
	}
	require.True(t, found, "the root must be a broadcast candidate, or this proves nothing")

	mockClient := &mockBitcoinClient{response: txHash}
	err = CheckExpiredTimeLocks(t.Context(), tc.Client, mockClient, root, 100_000, btcnetwork.Regtest)
	require.NoError(t, err)
	assert.Nil(t, mockClient.seenTX, "a consolidated root must never be broadcast, however long the timelock has been expired")
}

// TestCheckExpiredTimeLocksConsolidatedRefundAlreadyConfirmed covers the
// sibling of the dispatch's broadcast arm: an exit package already mined needs
// no rebroadcast.
func TestCheckExpiredTimeLocksConsolidatedRefundAlreadyConfirmed(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	node, err := dbClient.TreeNode.UpdateOne(f.node).SetRefundConfirmationHeight(1_010).Save(ctx)
	require.NoError(t, err)

	mockClient := &mockBitcoinClient{response: txHash}
	err = CheckExpiredTimeLocks(ctx, dbClient, mockClient, node, int64(1_000+uint64(spark.DirectTimelockOffset)), btcnetwork.Regtest)
	require.NoError(t, err)
	assert.Nil(t, mockClient.seenTX, "an already-confirmed exit package must not be rebroadcast")
}

// TestCheckAndBroadcastConsolidatedRefundRejectsForeignFundingOutpoint pins
// that the parent's confirmation height only starts this package's timelock
// when it is the confirmation of the output the package actually spends.
//
// node_confirmation_height is set by either the parent's raw node tx or its
// direct tx confirming, but a consolidated package only ever spends the raw
// one — so without this check a direct-tx parent exit would start the clock off
// a transaction the package does not spend and broadcast at an outpoint that
// never existed.
func TestCheckAndBroadcastConsolidatedRefundRejectsForeignFundingOutpoint(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := createConsolidatedFixture(t, ctx, 1_000)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Repoint the stored package at an outpoint the parent's node tx does not
	// produce, standing in for a package funded by some other transaction.
	tx, err := common.TxFromRawTxBytes(f.node.DirectFromCpfpRefundTx)
	require.NoError(t, err)
	tx.TxIn[0].PreviousOutPoint.Hash = st.NewRandomTxIDForTesting(t).Hash()
	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	node, err := dbClient.TreeNode.UpdateOne(f.node).SetDirectFromCpfpRefundTx(raw).Save(ctx)
	require.NoError(t, err)

	mockClient := &mockBitcoinClient{response: txHash}
	err = checkAndBroadcastConsolidatedRefundTx(ctx, mockClient, node, int64(1_000+uint64(spark.DirectTimelockOffset)), btcnetwork.Regtest)
	require.ErrorContains(t, err, "parent's confirmed node tx output is")
	assert.Nil(t, mockClient.seenTX, "a package not funded by the parent's node tx must not be broadcast")
}
