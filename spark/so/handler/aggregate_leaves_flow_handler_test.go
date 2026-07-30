package handler

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Tests in this file split between db.NewTestSQLiteContext and
// db.ConnectToTestPostgres on purpose: any test that exercises a code path
// taking row locks (loadAggregateLeavesSubtree with forUpdate=true, and so
// everything reached through Prepare, applyAggregateLeavesCommit,
// resolveCommitInputs, or Rollback) must use Postgres, because SQLite rejects
// SELECT ... FOR UPDATE. The pure read/validation helpers run on SQLite.

// aggregateLeavesFixture is a two-leaf subtree with internally consistent
// keys: leaf verifying keys sum to the target's verifying key, and the test
// holds every private key so it can produce valid final signatures.
type aggregateLeavesFixture struct {
	tree   *ent.Tree
	parent *ent.TreeNode // the target's parent (holds the tx funding the target)
	target *ent.TreeNode
	leaves []*ent.TreeNode

	parentTx       *wire.MsgTx
	targetKeyshare *ent.SigningKeyshare
	leafKeyshares  []*ent.SigningKeyshare

	leafUserPrivs     []keys.Private
	leafKeysharePrivs []keys.Private
	verifyingPriv     keys.Private // sum of all leaf user + keyshare privs
	ownerIdentity     keys.Private

	targetValue int64
}

func (f *aggregateLeavesFixture) aggregatedUserKey() keys.Public {
	sum := f.leafUserPrivs[0].Public()
	for _, p := range f.leafUserPrivs[1:] {
		sum = sum.Add(p.Public())
	}
	return sum
}

func (f *aggregateLeavesFixture) leafIDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(f.leaves))
	for i, l := range f.leaves {
		ids[i] = l.ID
	}
	return ids
}

func createAggregateLeavesKeyshare(t *testing.T, ctx context.Context, priv keys.Private, pubShare keys.Public) *ent.SigningKeyshare {
	t.Helper()
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(priv).
		SetPublicShares(map[string]keys.Public{"operator1": pubShare}).
		SetPublicKey(priv.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	return keyshare
}

// corruptTargetVerifyingKey makes the fixture's target carry a verifying key
// unrelated to its leaves, so the sum(leaf verifying keys) invariant fails
// while every other field stays consistent. verifying_pubkey is immutable, so
// this can only be done at creation.
func corruptTargetVerifyingKey(o *aggregateLeavesFixtureOptions) { o.corruptTargetVerifyingKey = true }

type aggregateLeavesFixtureOptions struct {
	corruptTargetVerifyingKey bool
}

func createAggregateLeavesFixture(t *testing.T, ctx context.Context, rng *rand.ChaCha8, opts ...func(*aggregateLeavesFixtureOptions)) *aggregateLeavesFixture {
	var options aggregateLeavesFixtureOptions
	for _, opt := range opts {
		opt(&options)
	}
	t.Helper()
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestRenewTree(t, ctx, ownerIdentity.Public())

	u1 := keys.MustGeneratePrivateKeyFromRand(rng)
	u2 := keys.MustGeneratePrivateKeyFromRand(rng)
	k1 := keys.MustGeneratePrivateKeyFromRand(rng)
	k2 := keys.MustGeneratePrivateKeyFromRand(rng)
	v1 := u1.Add(k1)
	v2 := u2.Add(k2)
	verifyingPriv := v1.Add(v2)

	const targetValue = int64(100_000)

	// The target's parent: its raw tx funds the target's defining outpoint at
	// vout 1, paying the target's verifying key.
	parentKeyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	parentTx := wire.NewMsgTx(3)
	parentTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: tree.BaseTxid.Hash(), Index: uint32(tree.Vout)}})
	addRenewVoutResetOutput(t, parentTx, 33_333, keys.MustGeneratePrivateKeyFromRand(rng).Public())
	addRenewVoutResetOutput(t, parentTx, targetValue, verifyingPriv.Public())
	parentTx.AddTxOut(common.EphemeralAnchorOutput())
	parentRaw, err := common.SerializeTx(parentTx)
	require.NoError(t, err)

	parent, err := dbClient.TreeNode.Create().
		SetTree(tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(parentKeyshare).
		SetValue(uint64(targetValue)).
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(ownerIdentity.Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(parentRaw).
		SetVout(0).
		SetStatus(st.TreeNodeStatusSplitted).
		Save(ctx)
	require.NoError(t, err)

	// The target: a split tx spending parentTx:1 into two outputs paying the
	// leaf verifying keys.
	targetKeyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	targetTx := wire.NewMsgTx(3)
	targetTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 1}})
	addRenewVoutResetOutput(t, targetTx, 60_000, v1.Public())
	addRenewVoutResetOutput(t, targetTx, 40_000, v2.Public())
	targetTx.AddTxOut(common.EphemeralAnchorOutput())
	targetRaw, err := common.SerializeTx(targetTx)
	require.NoError(t, err)

	targetDirectTx := wire.NewMsgTx(3)
	targetDirectTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 1}, Sequence: spark.DirectTimelockOffset})
	addRenewVoutResetOutput(t, targetDirectTx, common.MaybeApplyFee(60_000), v1.Public())
	addRenewVoutResetOutput(t, targetDirectTx, common.MaybeApplyFee(40_000), v2.Public())
	targetDirectRaw, err := common.SerializeTx(targetDirectTx)
	require.NoError(t, err)

	targetVerifyingKey := verifyingPriv.Public()
	if options.corruptTargetVerifyingKey {
		targetVerifyingKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
	}
	target, err := dbClient.TreeNode.Create().
		SetTree(tree).
		SetParent(parent).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(targetKeyshare).
		SetValue(uint64(targetValue)).
		SetVerifyingPubkey(targetVerifyingKey).
		SetOwnerIdentityPubkey(ownerIdentity.Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(targetRaw).
		SetDirectTx(targetDirectRaw).
		SetVout(1).
		SetStatus(st.TreeNodeStatusSplitted).
		Save(ctx)
	require.NoError(t, err)

	pubShare1 := keys.MustGeneratePrivateKeyFromRand(rng)
	pubShare2 := keys.MustGeneratePrivateKeyFromRand(rng)
	ks1 := createAggregateLeavesKeyshare(t, ctx, k1, pubShare1.Public())
	ks2 := createAggregateLeavesKeyshare(t, ctx, k2, pubShare2.Public())

	leaves := make([]*ent.TreeNode, 2)
	leafData := []struct {
		userPriv keys.Private
		keyshare *ent.SigningKeyshare
		verify   keys.Public
		value    int64
		vout     int
	}{
		{u1, ks1, v1.Public(), 60_000, 0},
		{u2, ks2, v2.Public(), 40_000, 1},
	}
	for i, d := range leafData {
		leafNodeTx := buildRenewVoutResetOutputTx(t, wire.OutPoint{Hash: targetTx.TxHash(), Index: uint32(d.vout)}, spark.InitialTimeLock, d.value, d.verify, true)
		leafRaw, err := common.SerializeTx(leafNodeTx)
		require.NoError(t, err)
		leafRefundTx := buildRenewVoutResetOutputTx(t, wire.OutPoint{Hash: leafNodeTx.TxHash(), Index: 0}, spark.InitialTimeLock, d.value, d.userPriv.Public(), true)
		leafRefundRaw, err := common.SerializeTx(leafRefundTx)
		require.NoError(t, err)

		leaves[i], err = dbClient.TreeNode.Create().
			SetTree(tree).
			SetParent(target).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(d.keyshare).
			SetValue(uint64(d.value)).
			SetVerifyingPubkey(d.verify).
			SetOwnerIdentityPubkey(ownerIdentity.Public()).
			SetOwnerSigningPubkey(d.userPriv.Public()).
			SetRawTx(leafRaw).
			SetRawRefundTx(leafRefundRaw).
			SetVout(int16(d.vout)).
			SetStatus(st.TreeNodeStatusAvailable).
			Save(ctx)
		require.NoError(t, err)
	}

	return &aggregateLeavesFixture{
		tree:              tree,
		parent:            parent,
		target:            target,
		leaves:            leaves,
		parentTx:          parentTx,
		targetKeyshare:    targetKeyshare,
		leafKeyshares:     []*ent.SigningKeyshare{ks1, ks2},
		leafUserPrivs:     []keys.Private{u1, u2},
		leafKeysharePrivs: []keys.Private{k1, k2},
		verifyingPriv:     verifyingPriv,
		ownerIdentity:     ownerIdentity,
		targetValue:       targetValue,
	}
}

// buildAggregateLeavesUserTxs builds the correctly shaped exit package the
// way a well-behaved client would.
func buildAggregateLeavesUserTxs(t *testing.T, f *aggregateLeavesFixture) (refundRaw, watchtowerRaw []byte) {
	t.Helper()
	exitKey := f.aggregatedUserKey()
	prevOutpoint := wire.OutPoint{Hash: f.parentTx.TxHash(), Index: 1}

	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: prevOutpoint, Sequence: spark.ZeroTimelock})
	addRenewVoutResetOutput(t, refundTx, f.targetValue, exitKey)
	refundTx.AddTxOut(common.EphemeralAnchorOutput())
	refundRaw, err := common.SerializeTx(refundTx)
	require.NoError(t, err)

	watchtowerTx := wire.NewMsgTx(3)
	watchtowerTx.AddTxIn(&wire.TxIn{PreviousOutPoint: prevOutpoint, Sequence: spark.DirectTimelockOffset})
	addRenewVoutResetOutput(t, watchtowerTx, common.MaybeApplyFee(f.targetValue), exitKey)
	watchtowerRaw, err = common.SerializeTx(watchtowerTx)
	require.NoError(t, err)
	return refundRaw, watchtowerRaw
}

func TestLoadAggregateLeavesSubtreeShapes(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{7})
	f := createAggregateLeavesFixture(t, ctx, rng)

	t.Run("happy two leaves", func(t *testing.T) {
		subtree, err := loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
		require.NoError(t, err)
		assert.Equal(t, f.target.ID, subtree.target.ID)
		assert.Empty(t, subtree.intermediates)
		require.Len(t, subtree.leaves, 2)
		assert.Equal(t, f.leaves[0].ID, subtree.leaves[0].ID)
		assert.Equal(t, f.leaves[1].ID, subtree.leaves[1].ID)
	})

	t.Run("incomplete leaf set rejected", func(t *testing.T) {
		_, err := loadAggregateLeavesSubtree(ctx, f.target.ID, []uuid.UUID{f.leaves[0].ID, uuid.New()}, false)
		require.ErrorContains(t, err, "complete leaf set")
	})

	t.Run("foreign leaf rejected", func(t *testing.T) {
		_, err := loadAggregateLeavesSubtree(ctx, f.target.ID, []uuid.UUID{f.leaves[0].ID, f.leaves[1].ID, uuid.New()}, false)
		require.ErrorContains(t, err, "not part of target")
	})

	t.Run("non-branching target rejected", func(t *testing.T) {
		_, err := loadAggregateLeavesSubtree(ctx, f.leaves[0].ID, f.leafIDs(), false)
		require.ErrorContains(t, err, "must be a branching node")
	})

	// A leaf that already absorbed its own subtree keeps children, and the walk
	// must stay able to resolve it through the whole flow's lifecycle. The
	// post-commit AGGREGATED state is the one that makes retries work: the
	// entrypoint and Commit both load the subtree before reaching their
	// idempotency short-circuits, so failing here would turn a redelivered
	// gossip commit into an error that is retried forever.
	t.Run("requested leaf with children resolves in every flow state", func(t *testing.T) {
		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		// Give leaf 0 a child so it looks like a previously-aggregated node.
		_, err = dbClient.TreeNode.Create().
			SetTree(f.tree).
			SetParent(f.leaves[0]).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(f.leafKeyshares[0]).
			SetValue(f.leaves[0].Value).
			SetVerifyingPubkey(f.leaves[0].VerifyingPubkey).
			SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
			SetOwnerSigningPubkey(f.leafUserPrivs[0].Public()).
			SetRawTx(f.leaves[0].RawTx).
			SetVout(0).
			SetStatus(st.TreeNodeStatusAggregated).
			Save(ctx)
		require.NoError(t, err)

		for _, status := range []st.TreeNodeStatus{
			st.TreeNodeStatusConsolidated,
			st.TreeNodeStatusAggregateLock,
			st.TreeNodeStatusAggregated,
		} {
			_, err := dbClient.TreeNode.UpdateOne(f.leaves[0]).SetStatus(status).Save(ctx)
			require.NoError(t, err)
			subtree, err := loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
			require.NoError(t, err, "a requested leaf in %s must stay resolvable", status)
			require.Len(t, subtree.leaves, 2)
			assert.Equal(t, f.leaves[0].ID, subtree.leaves[0].ID)
		}

		// Any other with-children status is still descended through rather than
		// silently retired as a leaf.
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
		require.NoError(t, err)
		_, err = loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
		require.ErrorContains(t, err, "may be aggregated as a leaf")
	})
}

func TestLoadAggregateLeavesSubtreeRenewChain(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{8})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Splice a renew-style one-child split node between the target and leaf 0.
	intermediate, err := dbClient.TreeNode.Create().
		SetTree(f.tree).
		SetParent(f.target).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(f.leafKeyshares[0]).
		SetValue(f.leaves[0].Value).
		SetVerifyingPubkey(f.leaves[0].VerifyingPubkey).
		SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
		SetOwnerSigningPubkey(f.leafUserPrivs[0].Public()).
		SetRawTx(f.leaves[0].RawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusSplitLocked).
		Save(ctx)
	require.NoError(t, err)
	_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetParent(intermediate).Save(ctx)
	require.NoError(t, err)

	subtree, err := loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
	require.NoError(t, err)
	require.Len(t, subtree.intermediates, 1)
	assert.Equal(t, intermediate.ID, subtree.intermediates[0].ID)
	require.Len(t, subtree.leaves, 2)

	// A second branching level under the target is rejected: give the
	// intermediate a second child so it branches.
	extraLeaf, err := dbClient.TreeNode.Create().
		SetTree(f.tree).
		SetParent(intermediate).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(f.leafKeyshares[0]).
		SetValue(1_000).
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(f.leaves[0].RawTx).
		SetVout(1).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)
	_, err = loadAggregateLeavesSubtree(ctx, f.target.ID, append(f.leafIDs(), extraLeaf.ID), false)
	require.ErrorContains(t, err, "only one branching level")
}

func TestValidateAggregateLeavesSubtreeChecks(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{9})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	subtree, err := loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
	require.NoError(t, err)

	aggregated, err := validateAggregateLeavesSubtree(ctx, subtree, f.ownerIdentity.Public())
	require.NoError(t, err)
	assert.True(t, aggregated.Equals(f.aggregatedUserKey()))

	t.Run("foreign owner rejected", func(t *testing.T) {
		_, err := validateAggregateLeavesSubtree(ctx, subtree, keys.MustGeneratePrivateKeyFromRand(rng).Public())
		require.ErrorContains(t, err, "not owned by the initiator")
	})

	t.Run("unavailable leaf rejected", func(t *testing.T) {
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetStatus(st.TreeNodeStatusTransferLocked).Save(ctx)
		require.NoError(t, err)
		reloaded, err := loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
		require.NoError(t, err)
		_, err = validateAggregateLeavesSubtree(ctx, reloaded, f.ownerIdentity.Public())
		require.ErrorContains(t, err, "expected AVAILABLE or CONSOLIDATED")
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
		require.NoError(t, err)
	})

	t.Run("leaf without refund tx rejected", func(t *testing.T) {
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).ClearRawRefundTx().Save(ctx)
		require.NoError(t, err)
		reloaded, err := loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
		require.NoError(t, err)
		_, err = validateAggregateLeavesSubtree(ctx, reloaded, f.ownerIdentity.Public())
		require.ErrorContains(t, err, "has no refund tx")
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetRawRefundTx(f.leaves[0].RawRefundTx).Save(ctx)
		require.NoError(t, err)
	})

	// The two key-sum invariants fail with distinct messages, so each test
	// asserts the specific one it breaks.
	t.Run("user key and keyshare sum mismatch rejected", func(t *testing.T) {
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).Save(ctx)
		require.NoError(t, err)
		reloaded, err := loadAggregateLeavesSubtree(ctx, f.target.ID, f.leafIDs(), false)
		require.NoError(t, err)
		_, err = validateAggregateLeavesSubtree(ctx, reloaded, f.ownerIdentity.Public())
		require.ErrorContains(t, err, "sum of leaf user keys and keyshares does not equal target")
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetOwnerSigningPubkey(f.leafUserPrivs[0].Public()).Save(ctx)
		require.NoError(t, err)
	})

	t.Run("verifying key sum mismatch rejected", func(t *testing.T) {
		mismatched := createAggregateLeavesFixture(t, ctx, rng, corruptTargetVerifyingKey)
		reloaded, err := loadAggregateLeavesSubtree(ctx, mismatched.target.ID, mismatched.leafIDs(), false)
		require.NoError(t, err)
		_, err = validateAggregateLeavesSubtree(ctx, reloaded, mismatched.ownerIdentity.Public())
		require.ErrorContains(t, err, "sum of leaf verifying keys does not equal target")
	})
}

func TestConstructAggregateLeavesTransactions(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{10})
	f := createAggregateLeavesFixture(t, ctx, rng)
	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)

	makeJob := func(raw []byte) *pbspark.UserSignedTxSigningJob {
		return &pbspark.UserSignedTxSigningJob{RawTx: raw}
	}

	t.Run("well-formed package accepted", func(t *testing.T) {
		txs, err := constructAggregateLeavesTransactions(ctx, f.target, f.aggregatedUserKey(), makeJob(refundRaw), makeJob(watchtowerRaw))
		require.NoError(t, err)
		assert.Equal(t, f.targetValue, txs.PrevOut.Value)
		assert.Equal(t, f.targetValue, txs.RefundTx.TxOut[0].Value)
		assert.Equal(t, common.MaybeApplyFee(f.targetValue), txs.WatchtowerRefundTx.TxOut[0].Value)
		assert.Len(t, txs.RefundTx.TxOut, 2)
		assert.Len(t, txs.WatchtowerRefundTx.TxOut, 1)
	})

	t.Run("timelocked refund rejected", func(t *testing.T) {
		badTx := wire.NewMsgTx(3)
		badTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: f.parentTx.TxHash(), Index: 1}, Sequence: 100})
		addRenewVoutResetOutput(t, badTx, f.targetValue, f.aggregatedUserKey())
		badTx.AddTxOut(common.EphemeralAnchorOutput())
		badRaw, err := common.SerializeTx(badTx)
		require.NoError(t, err)
		_, err = constructAggregateLeavesTransactions(ctx, f.target, f.aggregatedUserKey(), makeJob(badRaw), makeJob(watchtowerRaw))
		require.ErrorContains(t, err, "must carry no timelock")
	})

	t.Run("value diversion rejected", func(t *testing.T) {
		badTx := wire.NewMsgTx(3)
		badTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: f.parentTx.TxHash(), Index: 1}, Sequence: spark.ZeroTimelock})
		addRenewVoutResetOutput(t, badTx, f.targetValue-1, f.aggregatedUserKey())
		badTx.AddTxOut(common.EphemeralAnchorOutput())
		badRaw, err := common.SerializeTx(badTx)
		require.NoError(t, err)
		_, err = constructAggregateLeavesTransactions(ctx, f.target, f.aggregatedUserKey(), makeJob(badRaw), makeJob(watchtowerRaw))
		require.ErrorContains(t, err, "user transaction validation failed")
	})

	t.Run("foreign destination rejected", func(t *testing.T) {
		badTx := wire.NewMsgTx(3)
		badTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: f.parentTx.TxHash(), Index: 1}, Sequence: spark.ZeroTimelock})
		addRenewVoutResetOutput(t, badTx, f.targetValue, keys.MustGeneratePrivateKeyFromRand(rng).Public())
		badTx.AddTxOut(common.EphemeralAnchorOutput())
		badRaw, err := common.SerializeTx(badTx)
		require.NoError(t, err)
		_, err = constructAggregateLeavesTransactions(ctx, f.target, f.aggregatedUserKey(), makeJob(badRaw), makeJob(watchtowerRaw))
		require.ErrorContains(t, err, "user transaction validation failed")
	})
}

func TestApplyAggregateLeavesCommit(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{11})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)
	txs, err := constructAggregateLeavesTransactions(ctx, f.target, f.aggregatedUserKey(), &pbspark.UserSignedTxSigningJob{RawTx: refundRaw}, &pbspark.UserSignedTxSigningJob{RawTx: watchtowerRaw})
	require.NoError(t, err)

	// Produce real final signatures with the summed private key.
	signedRefund := signRenewVoutResetTx(t, txs.RefundTx, txs.PrevOut, f.verifyingPriv)
	_, signedRefundBytes, err := applyAndVerifySignature(txs.RefundTx, signedRefund, txs.PrevOut, 0)
	require.NoError(t, err)
	signedWatchtower := signRenewVoutResetTx(t, txs.WatchtowerRefundTx, txs.PrevOut, f.verifyingPriv)
	_, signedWatchtowerBytes, err := applyAndVerifySignature(txs.WatchtowerRefundTx, signedWatchtower, txs.PrevOut, 0)
	require.NoError(t, err)

	// Simulate the post-Prepare state.
	for _, leaf := range f.leaves {
		_, err := dbClient.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAggregateLock).Save(ctx)
		require.NoError(t, err)
	}

	// Give the target a threshold that disagrees with the leaves', so the
	// assertion below proves the rotation carries the leaves' threshold onto
	// the row rather than leaving the target's own stale value next to the
	// summed key material.
	_, err = dbClient.SigningKeyshare.UpdateOne(f.targetKeyshare).SetMinSigners(4).Save(ctx)
	require.NoError(t, err)

	commitReq := &pbinternal.AggregateLeavesCommitRequest{
		TargetNodeId:                    f.target.ID.String(),
		LeafIds:                         []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		SignedRefundTx:                  signedRefundBytes,
		SignedWatchtowerRefundTx:        signedWatchtowerBytes,
		AggregatedOwnerSigningPublicKey: f.aggregatedUserKey().Serialize(),
		OwnerIdentityPublicKey:          f.ownerIdentity.Public().Serialize(),
	}
	require.NoError(t, applyAggregateLeavesCommit(ctx, nil, commitReq))

	target, err := dbClient.TreeNode.Get(ctx, f.target.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusConsolidated, target.Status)
	assert.Equal(t, signedRefundBytes, target.RawRefundTx)
	assert.Equal(t, signedWatchtowerBytes, target.DirectFromCpfpRefundTx)
	assert.Empty(t, target.DirectTx)
	assert.Empty(t, target.DirectRefundTx)
	assert.True(t, target.OwnerSigningPubkey.Equals(f.aggregatedUserKey()))
	assert.True(t, target.OwnerIdentityPubkey.Equals(f.ownerIdentity.Public()))

	// The target's keyshare rotated to the sum of the leaf keyshares.
	rotated, err := dbClient.SigningKeyshare.Get(ctx, f.targetKeyshare.ID)
	require.NoError(t, err)
	expectedPub := f.leafKeysharePrivs[0].Public().Add(f.leafKeysharePrivs[1].Public())
	assert.True(t, rotated.PublicKey.Equals(expectedPub))
	rotatedSecret, err := rotated.GetSecretShare(ctx)
	require.NoError(t, err)
	assert.True(t, rotatedSecret.Public().Equals(expectedPub))
	assert.Equal(t, f.leafKeyshares[0].MinSigners, rotated.MinSigners,
		"the rotated row must declare the threshold its summed material was built under")

	// Retired descendants keep their transactions: AGGREGATED already stops
	// the watchtower from broadcasting them, and retaining the bytes leaves a
	// recovery path if the target's old node tx ever confirms and kills the
	// consolidated package.
	for i, leaf := range f.leaves {
		reloaded, err := dbClient.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAggregated, reloaded.Status)
		assert.Equal(t, f.leaves[i].RawRefundTx, reloaded.RawRefundTx, "retired leaf must keep its refund tx")
		assert.NotEmpty(t, reloaded.RawTx)
	}

	// Idempotent redelivery is a no-op.
	require.NoError(t, applyAggregateLeavesCommit(ctx, nil, commitReq))

	// Both slots are compared before a redelivery counts as the same decision,
	// so a mismatch in either one against a consolidated target is rejected
	// rather than silently accepted as a no-op.
	otherRefund, ok := proto.Clone(commitReq).(*pbinternal.AggregateLeavesCommitRequest)
	require.True(t, ok)
	otherRefund.SignedRefundTx = signedWatchtowerBytes
	require.ErrorContains(t, applyAggregateLeavesCommit(ctx, nil, otherRefund), "different exit package")

	otherWatchtower, ok := proto.Clone(commitReq).(*pbinternal.AggregateLeavesCommitRequest)
	require.True(t, ok)
	otherWatchtower.SignedWatchtowerRefundTx = signedRefundBytes
	require.ErrorContains(t, applyAggregateLeavesCommit(ctx, nil, otherWatchtower), "different exit package")
}

// TestApplyAggregateLeavesCommitDeclinesWhenSubtreeWentOnChain pins that a
// subtree with any on-chain node is not consolidated at all.
//
// The exit package spends the same outpoint the target's own node tx spends, so
// a descendant can only have confirmed if that node tx already won the spend —
// the package is dead on arrival. Consolidating anyway would install an
// unspendable package, retire the leaves, and clear the direct tx that is the
// actual remaining way out. Prepare rejects these statuses, so this is the
// delayed-gossip-redelivery case: the chain watcher recorded a confirmation
// after Prepare. The decline must surface as AlreadyExists, because gossip
// treats that as success and marks the participant row terminal, while any
// other error is redelivered forever.
func TestApplyAggregateLeavesCommitDeclinesWhenSubtreeWentOnChain(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{43})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// A one-child renew intermediate whose tx the chain watcher already saw.
	intermediate, err := dbClient.TreeNode.Create().
		SetTree(f.tree).
		SetParent(f.target).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(f.leafKeyshares[0]).
		SetValue(f.leaves[0].Value).
		SetVerifyingPubkey(f.leaves[0].VerifyingPubkey).
		SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
		SetOwnerSigningPubkey(f.leafUserPrivs[0].Public()).
		SetRawTx(f.leaves[0].RawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusOnChain).
		Save(ctx)
	require.NoError(t, err)
	_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetParent(intermediate).Save(ctx)
	require.NoError(t, err)

	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)
	txs, err := constructAggregateLeavesTransactions(ctx, f.target, f.aggregatedUserKey(), &pbspark.UserSignedTxSigningJob{RawTx: refundRaw}, &pbspark.UserSignedTxSigningJob{RawTx: watchtowerRaw})
	require.NoError(t, err)
	_, signedRefundBytes, err := applyAndVerifySignature(txs.RefundTx, signRenewVoutResetTx(t, txs.RefundTx, txs.PrevOut, f.verifyingPriv), txs.PrevOut, 0)
	require.NoError(t, err)
	_, signedWatchtowerBytes, err := applyAndVerifySignature(txs.WatchtowerRefundTx, signRenewVoutResetTx(t, txs.WatchtowerRefundTx, txs.PrevOut, f.verifyingPriv), txs.PrevOut, 0)
	require.NoError(t, err)

	for _, leaf := range f.leaves {
		_, err := dbClient.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAggregateLock).Save(ctx)
		require.NoError(t, err)
	}

	err = applyAggregateLeavesCommit(ctx, nil, &pbinternal.AggregateLeavesCommitRequest{
		TargetNodeId:                    f.target.ID.String(),
		LeafIds:                         []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		SignedRefundTx:                  signedRefundBytes,
		SignedWatchtowerRefundTx:        signedWatchtowerBytes,
		AggregatedOwnerSigningPublicKey: f.aggregatedUserKey().Serialize(),
		OwnerIdentityPublicKey:          f.ownerIdentity.Public().Serialize(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err),
		"the decline must be AlreadyExists so gossip marks the row terminal instead of redelivering forever")
	require.ErrorContains(t, err, "already spent")

	// Nothing was consolidated, retired, or cleared.
	target, err := dbClient.TreeNode.Get(ctx, f.target.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusSplitted, target.Status, "the target must not be consolidated")
	assert.Empty(t, target.RawRefundTx, "no exit package may be installed")
	assert.NotEmpty(t, target.DirectTx, "the direct tx is the remaining way out and must survive")

	reloadedIntermediate, err := dbClient.TreeNode.Get(ctx, intermediate.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusOnChain, reloadedIntermediate.Status,
		"the chain watcher's observation must be preserved")
	for _, leaf := range f.leaves {
		reloaded, err := dbClient.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAvailable, reloaded.Status,
			"a declined commit must release the leaves it is abandoning, to their correct prior status")
	}
}

// TestApplyAggregateLeavesCommitRejectsForgedPackage covers what the
// participant fence cannot: commit re-derives the exit package's shape and
// owner key locally, so a coordinator cannot substitute another
// validly-signed transaction or a foreign owner key.
func TestApplyAggregateLeavesCommitRejectsForgedPackage(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{15})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)
	txs, err := constructAggregateLeavesTransactions(ctx, f.target, f.aggregatedUserKey(), &pbspark.UserSignedTxSigningJob{RawTx: refundRaw}, &pbspark.UserSignedTxSigningJob{RawTx: watchtowerRaw})
	require.NoError(t, err)
	_, signedRefundBytes, err := applyAndVerifySignature(txs.RefundTx, signRenewVoutResetTx(t, txs.RefundTx, txs.PrevOut, f.verifyingPriv), txs.PrevOut, 0)
	require.NoError(t, err)
	_, signedWatchtowerBytes, err := applyAndVerifySignature(txs.WatchtowerRefundTx, signRenewVoutResetTx(t, txs.WatchtowerRefundTx, txs.PrevOut, f.verifyingPriv), txs.PrevOut, 0)
	require.NoError(t, err)

	for _, leaf := range f.leaves {
		_, err := dbClient.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAggregateLock).Save(ctx)
		require.NoError(t, err)
	}
	base := &pbinternal.AggregateLeavesCommitRequest{
		TargetNodeId:                    f.target.ID.String(),
		LeafIds:                         []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		SignedRefundTx:                  signedRefundBytes,
		SignedWatchtowerRefundTx:        signedWatchtowerBytes,
		AggregatedOwnerSigningPublicKey: f.aggregatedUserKey().Serialize(),
		OwnerIdentityPublicKey:          f.ownerIdentity.Public().Serialize(),
	}
	clone := func(mutate func(*pbinternal.AggregateLeavesCommitRequest)) *pbinternal.AggregateLeavesCommitRequest {
		req, ok := proto.Clone(base).(*pbinternal.AggregateLeavesCommitRequest)
		require.True(t, ok)
		mutate(req)
		return req
	}

	t.Run("forged aggregated owner key rejected", func(t *testing.T) {
		req := clone(func(r *pbinternal.AggregateLeavesCommitRequest) {
			r.AggregatedOwnerSigningPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
		})
		require.ErrorContains(t, applyAggregateLeavesCommit(ctx, nil, req), "does not equal the sum of the leaf signing keys")
	})

	t.Run("swapped slots rejected", func(t *testing.T) {
		req := clone(func(r *pbinternal.AggregateLeavesCommitRequest) {
			r.SignedRefundTx, r.SignedWatchtowerRefundTx = signedWatchtowerBytes, signedRefundBytes
		})
		require.ErrorContains(t, applyAggregateLeavesCommit(ctx, nil, req), "timelock")
	})

	t.Run("substituted target node tx rejected", func(t *testing.T) {
		req := clone(func(r *pbinternal.AggregateLeavesCommitRequest) {
			r.SignedRefundTx = f.target.RawTx
		})
		// Pinned to the shape check that catches it: the target's own node tx
		// carries a valid signature over the very same outpoint and verifying
		// key, so only its shape (a split tx's outputs) distinguishes it.
		require.ErrorContains(t, applyAggregateLeavesCommit(ctx, nil, req), "signed refund tx has 3 outputs, expected 2")
	})

	// The subtree must survive every rejection unchanged.
	target, err := dbClient.TreeNode.Get(ctx, f.target.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusSplitted, target.Status)
	for _, leaf := range f.leaves {
		reloaded, err := dbClient.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.NotEmpty(t, reloaded.RawRefundTx)
	}
}

// TestAggregateLeavesPrevOutpointRoot covers the root branch, where the exit
// outpoint comes from the tree's deposit rather than a parent's tx.
//
// The multi-input case is the one that matters: a multi-UTXO deposit root's
// node tx spends several UTXOs and its value is their sum, while
// tree.base_txid/vout names only the primary one. Deriving the prevout from
// those row fields alone would claim the summed value at an outpoint holding
// just one UTXO — identically on every operator, so the package would pass
// every local shape and signature check and still be unspendable on-chain.
func TestAggregateLeavesPrevOutpointRoot(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{41})
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestRenewTree(t, ctx, ownerIdentity.Public())
	verifyingPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	const rootValue = int64(100_000)

	// extraInputs > 0 models a multi-UTXO deposit root.
	newRoot := func(t *testing.T, extraInputs int) *ent.TreeNode {
		t.Helper()
		rootTx := wire.NewMsgTx(3)
		rootTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: tree.BaseTxid.Hash(), Index: uint32(tree.Vout)}})
		for range extraInputs {
			rootTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: st.NewRandomTxIDForTesting(t).Hash(), Index: 0}})
		}
		addRenewVoutResetOutput(t, rootTx, rootValue, keys.MustGeneratePrivateKeyFromRand(rng).Public())
		rootTx.AddTxOut(common.EphemeralAnchorOutput())
		rootRaw, err := common.SerializeTx(rootTx)
		require.NoError(t, err)

		root, err := dbClient.TreeNode.Create().
			SetTree(tree).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(createTestRenewSigningKeyshare(t, ctx, rng)).
			SetValue(uint64(rootValue)).
			SetVerifyingPubkey(verifyingPriv.Public()).
			SetOwnerIdentityPubkey(ownerIdentity.Public()).
			SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetRawTx(rootRaw).
			SetVout(tree.Vout).
			SetStatus(st.TreeNodeStatusSplitted).
			Save(ctx)
		require.NoError(t, err)
		return root
	}

	t.Run("single utxo root spends the deposit outpoint", func(t *testing.T) {
		root := newRoot(t, 0)
		outpoint, prevOut, err := aggregateLeavesPrevOutpoint(ctx, root)
		require.NoError(t, err)
		assert.Equal(t, tree.BaseTxid.Hash(), outpoint.Hash)
		assert.Equal(t, uint32(tree.Vout), outpoint.Index)
		assert.Equal(t, rootValue, prevOut.Value)
	})

	t.Run("multi utxo deposit root rejected", func(t *testing.T) {
		root := newRoot(t, 1)
		_, _, err := aggregateLeavesPrevOutpoint(ctx, root)
		require.ErrorContains(t, err, "single-input node tx")
	})
}

func TestAggregateLeavesRollback(t *testing.T) {
	// Each subtest builds its own fixture: rollback outcomes depend on node
	// status, so sharing one would couple the subtests' ordering.
	setup := func(t *testing.T, seed byte) (context.Context, *ent.Client, *aggregateLeavesFixture, func()) {
		t.Helper()
		ctx, _ := db.ConnectToTestPostgres(t)
		f := createAggregateLeavesFixture(t, ctx, rand.NewChaCha8([32]byte{seed}))
		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		lockAll := func() {
			for _, leaf := range f.leaves {
				_, err := dbClient.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAggregateLock).Save(ctx)
				require.NoError(t, err)
			}
		}
		return ctx, dbClient, f, lockAll
	}
	handler := NewAggregateLeavesFlowHandler(nil)

	t.Run("prepare echo derives statuses from shape", func(t *testing.T) {
		ctx, dbClient, f, lockAll := setup(t, 12)
		lockAll()
		err := handler.Rollback(ctx, &pbinternal.AggregateLeavesPrepareRequest{
			TargetNodeId: f.target.ID.String(),
			LeafIds:      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		})
		require.NoError(t, err)
		target, err := dbClient.TreeNode.Get(ctx, f.target.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusSplitted, target.Status)
		for _, leaf := range f.leaves {
			reloaded, err := dbClient.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			assert.Equal(t, st.TreeNodeStatusAvailable, reloaded.Status)
		}
	})

	t.Run("parent that exited while locked yields PARENT_EXITED", func(t *testing.T) {
		ctx, dbClient, f, lockAll := setup(t, 33)
		lockAll()
		// Leaf 1 already absorbed a subtree, so it was CONSOLIDATED pre-lock.
		_, err := dbClient.TreeNode.Create().
			SetTree(f.tree).
			SetParent(f.leaves[1]).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(f.leafKeyshares[1]).
			SetValue(f.leaves[1].Value).
			SetVerifyingPubkey(f.leaves[1].VerifyingPubkey).
			SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
			SetOwnerSigningPubkey(f.leafUserPrivs[1].Public()).
			SetRawTx(f.leaves[1].RawTx).
			SetVout(0).
			SetStatus(st.TreeNodeStatusAggregated).
			Save(ctx)
		require.NoError(t, err)

		_, err = dbClient.TreeNode.UpdateOne(f.target).
			SetStatus(st.TreeNodeStatusOnChain).
			SetNodeConfirmationHeight(1_000).
			Save(ctx)
		require.NoError(t, err)

		require.NoError(t, handler.Rollback(ctx, &pbinternal.AggregateLeavesRollbackRequest{
			TargetNodeId: f.target.ID.String(),
			LeafIds:      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		}))

		plain, err := dbClient.TreeNode.Get(ctx, f.leaves[0].ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusParentExited, plain.Status,
			"a leaf whose parent exited while it was locked must not come back AVAILABLE")

		consolidated, err := dbClient.TreeNode.Get(ctx, f.leaves[1].ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusConsolidated, consolidated.Status,
			"a consolidated leaf must keep the status its exit package is found by, even under an exited parent")
	})

	t.Run("parent exited by refund confirmation also yields PARENT_EXITED", func(t *testing.T) {
		ctx, dbClient, f, lockAll := setup(t, 34)
		lockAll()
		_, err := dbClient.TreeNode.UpdateOne(f.target).
			SetStatus(st.TreeNodeStatusExited).
			SetRefundConfirmationHeight(1_000).
			Save(ctx)
		require.NoError(t, err)
		reloadedTarget, err := dbClient.TreeNode.Get(ctx, f.target.ID)
		require.NoError(t, err)
		require.Zero(t, reloadedTarget.NodeConfirmationHeight, "this case is only meaningful with the node height unset")

		require.NoError(t, handler.Rollback(ctx, &pbinternal.AggregateLeavesRollbackRequest{
			TargetNodeId: f.target.ID.String(),
			LeafIds:      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		}))

		for _, leaf := range f.leaves {
			reloaded, err := dbClient.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			assert.Equal(t, st.TreeNodeStatusParentExited, reloaded.Status,
				"a parent that exited by refund confirmation must count as exited")
		}
	})

	t.Run("intermediates and target are never locked, so nothing to restore", func(t *testing.T) {
		ctx, dbClient, f, lockAll := setup(t, 16)
		// A renew-created split node between the target and a leaf rests at
		// SPLIT_LOCKED. The flow must not touch it at all — restoring it from
		// a guess (or from another operator's view) could move it to SPLITTED,
		// which the watchtower treats as terminal.
		intermediate, err := dbClient.TreeNode.Create().
			SetTree(f.tree).
			SetParent(f.target).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(f.leafKeyshares[0]).
			SetValue(f.leaves[0].Value).
			SetVerifyingPubkey(f.leaves[0].VerifyingPubkey).
			SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
			SetOwnerSigningPubkey(f.leafUserPrivs[0].Public()).
			SetRawTx(f.leaves[0].RawTx).
			SetVout(0).
			SetStatus(st.TreeNodeStatusSplitLocked).
			Save(ctx)
		require.NoError(t, err)
		_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetParent(intermediate).Save(ctx)
		require.NoError(t, err)
		lockAll()

		require.NoError(t, handler.Rollback(ctx, &pbinternal.AggregateLeavesPrepareRequest{
			TargetNodeId: f.target.ID.String(),
			LeafIds:      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		}))
		reloaded, err := dbClient.TreeNode.Get(ctx, intermediate.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusSplitLocked, reloaded.Status)
		target, err := dbClient.TreeNode.Get(ctx, f.target.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusSplitted, target.Status)
	})

	t.Run("a consolidated leaf is restored as CONSOLIDATED from its own shape", func(t *testing.T) {
		ctx, dbClient, f, lockAll := setup(t, 17)
		// A CONSOLIDATED leaf is one that already absorbed a subtree, so it
		// has children; that is what distinguishes it locally from an
		// ordinary AVAILABLE leaf.
		_, err := dbClient.TreeNode.Create().
			SetTree(f.tree).
			SetParent(f.leaves[0]).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(f.leafKeyshares[0]).
			SetValue(1).
			SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{20})).Public()).
			SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
			SetOwnerSigningPubkey(f.leafUserPrivs[0].Public()).
			SetRawTx(f.leaves[0].RawTx).
			SetVout(0).
			SetStatus(st.TreeNodeStatusAggregated).
			Save(ctx)
		require.NoError(t, err)
		lockAll()

		err = handler.Rollback(ctx, &pbinternal.AggregateLeavesRollbackRequest{
			TargetNodeId: f.target.ID.String(),
			LeafIds:      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		})
		require.NoError(t, err)
		leaf0, err := dbClient.TreeNode.Get(ctx, f.leaves[0].ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusConsolidated, leaf0.Status)
		leaf1, err := dbClient.TreeNode.Get(ctx, f.leaves[1].ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAvailable, leaf1.Status)
	})

	t.Run("nodes not locked are untouched", func(t *testing.T) {
		ctx, dbClient, f, _ := setup(t, 19)
		_, err := dbClient.TreeNode.UpdateOne(f.leaves[1]).SetStatus(st.TreeNodeStatusTransferLocked).Save(ctx)
		require.NoError(t, err)
		err = handler.Rollback(ctx, &pbinternal.AggregateLeavesRollbackRequest{
			TargetNodeId: f.target.ID.String(),
			LeafIds:      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		})
		require.NoError(t, err)
		leaf0, err := dbClient.TreeNode.Get(ctx, f.leaves[0].ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAvailable, leaf0.Status)
		leaf1, err := dbClient.TreeNode.Get(ctx, f.leaves[1].ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusTransferLocked, leaf1.Status)
	})
}

func TestAggregateLeavesValidateDecisionAgainstPrepare(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{14})
	f := createAggregateLeavesFixture(t, ctx, rng)
	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)

	handler := NewAggregateLeavesFlowHandler(nil)
	target := f.target.ID.String()
	leafA, leafB := f.leaves[0].ID.String(), f.leaves[1].ID.String()
	owner := f.ownerIdentity.Public().Serialize()
	prepare := &pbinternal.AggregateLeavesPrepareRequest{
		TargetNodeId:                 target,
		LeafIds:                      []string{leafA, leafB},
		OwnerIdentityPublicKey:       owner,
		RefundTxSigningJob:           &pbspark.UserSignedTxSigningJob{RawTx: refundRaw},
		WatchtowerRefundTxSigningJob: &pbspark.UserSignedTxSigningJob{RawTx: watchtowerRaw},
	}
	goodCommit := func() *pbinternal.AggregateLeavesCommitRequest {
		return &pbinternal.AggregateLeavesCommitRequest{
			TargetNodeId:             target,
			LeafIds:                  []string{leafB, leafA},
			OwnerIdentityPublicKey:   owner,
			SignedRefundTx:           refundRaw,
			SignedWatchtowerRefundTx: watchtowerRaw,
		}
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, goodCommit()))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.AggregateLeavesRollbackRequest{TargetNodeId: target, LeafIds: []string{leafA, leafB}}))

	err := handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.AggregateLeavesCommitRequest{TargetNodeId: uuid.New().String(), LeafIds: []string{leafA, leafB}})
	require.ErrorContains(t, err, "does not match prepared target")

	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.AggregateLeavesRollbackRequest{TargetNodeId: target, LeafIds: []string{leafA, uuid.New().String()}})
	require.ErrorContains(t, err, "not part of the prepared leaf set")

	t.Run("substituted tx rejected", func(t *testing.T) {
		// The target's own node tx spends the same outpoint under the same
		// verifying key, so a signature check alone would accept it.
		commit := goodCommit()
		commit.SignedRefundTx = f.target.RawTx
		require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, commit), "does not match the prepared one")
	})

	t.Run("swapped slots rejected", func(t *testing.T) {
		commit := goodCommit()
		commit.SignedRefundTx, commit.SignedWatchtowerRefundTx = watchtowerRaw, refundRaw
		require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, commit), "does not match the prepared one")
	})

	t.Run("forged owner identity rejected", func(t *testing.T) {
		commit := goodCommit()
		commit.OwnerIdentityPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
		require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, commit), "does not match the prepared owner")
	})

	// The fence requires exact multiset equality, not length plus membership.
	// A rollback naming [A, A] against a prepared [A, B] has the right length
	// and every id is a member, but acting on it would restore only A and
	// strand B in AGGREGATE_LOCK with no flow left to release it.
	t.Run("duplicate leaf rejected", func(t *testing.T) {
		err := handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.AggregateLeavesRollbackRequest{
			TargetNodeId: target,
			LeafIds:      []string{leafA, leafA},
		})
		require.ErrorContains(t, err, "named more than once")

		commit := goodCommit()
		commit.LeafIds = []string{leafB, leafB}
		require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, commit), "named more than once")
	})
}

// TestAggregateLeavesPrepareLocksLeavesOnly drives Prepare itself rather than
// its helpers, covering the two things only Prepare does: the leaf-locking
// write loop, and the early return for an SO outside the user's round-1
// commitment set.
//
// Naming a commitment set this operator is not in is what keeps the test off
// FROST — that SO has validated and locked, which is all Prepare requires of
// it, so the signing round never runs.
func TestAggregateLeavesPrepareLocksLeavesOnly(t *testing.T) {
	// Postgres: Prepare loads the subtree FOR UPDATE, which SQLite rejects.
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{42})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)

	// A one-child renew intermediate, so the test also pins that intermediates
	// are left untouched by the lock loop.
	intermediate, err := dbClient.TreeNode.Create().
		SetTree(f.tree).
		SetParent(f.target).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(f.leafKeyshares[0]).
		SetValue(f.leaves[0].Value).
		SetVerifyingPubkey(f.leaves[0].VerifyingPubkey).
		SetOwnerIdentityPubkey(f.ownerIdentity.Public()).
		SetOwnerSigningPubkey(f.leafUserPrivs[0].Public()).
		SetRawTx(f.leaves[0].RawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusSplitLocked).
		Save(ctx)
	require.NoError(t, err)
	_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).SetParent(intermediate).Save(ctx)
	require.NoError(t, err)

	// The commitment set names operator2, so operator1 is not a signer.
	commitments := &pbspark.SigningCommitments{
		SigningCommitments: map[string]*pbcommon.SigningCommitment{
			"operator2": frost.GenerateSigningNonce().SigningCommitment().MarshalProto(),
		},
	}
	job := func(rawTx []byte) *pbspark.UserSignedTxSigningJob {
		return &pbspark.UserSignedTxSigningJob{
			RawTx:                  rawTx,
			SigningPublicKey:       f.aggregatedUserKey().Serialize(),
			SigningNonceCommitment: frost.GenerateSigningNonce().SigningCommitment().MarshalProto(),
			UserSignature:          []byte{1},
			SigningCommitments:     commitments,
		}
	}
	handler := NewAggregateLeavesFlowHandler(&so.Config{Identifier: "operator1"})
	resp, err := handler.Prepare(ctx, &pbinternal.AggregateLeavesPrepareRequest{
		TargetNodeId:                 f.target.ID.String(),
		LeafIds:                      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		OwnerIdentityPublicKey:       f.ownerIdentity.Public().Serialize(),
		RefundTxSigningJob:           job(refundRaw),
		WatchtowerRefundTxSigningJob: job(watchtowerRaw),
	})
	require.NoError(t, err)
	assert.Nil(t, resp, "an SO outside the commitment set returns no round-2 shares")

	for _, leaf := range f.leaves {
		reloaded, err := dbClient.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAggregateLock, reloaded.Status, "every leaf must be locked")
	}

	// The target and intermediates keep their status: nothing to restore on
	// rollback, and a competing flow still stops at the locked leaves.
	reloadedTarget, err := dbClient.TreeNode.Get(ctx, f.target.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusSplitted, reloadedTarget.Status)
	reloadedIntermediate, err := dbClient.TreeNode.Get(ctx, intermediate.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusSplitLocked, reloadedIntermediate.Status)
}

func TestValidateAggregateLeavesUserJobs(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{13})
	aggKey := keys.MustGeneratePrivateKeyFromRand(rng)
	commitments := &pbspark.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{"operator1": {}}}
	goodJob := func() *pbspark.UserSignedTxSigningJob {
		return &pbspark.UserSignedTxSigningJob{
			SigningPublicKey:       aggKey.Public().Serialize(),
			SigningNonceCommitment: &pbcommon.SigningCommitment{},
			UserSignature:          []byte{1},
			SigningCommitments:     commitments,
		}
	}

	require.NoError(t, validateAggregateLeavesUserJobs(aggKey.Public(), goodJob(), goodJob()))

	wrongKey := goodJob()
	wrongKey.SigningPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	require.ErrorContains(t, validateAggregateLeavesUserJobs(aggKey.Public(), wrongKey, goodJob()), "does not equal the sum")

	noSig := goodJob()
	noSig.UserSignature = nil
	require.ErrorContains(t, validateAggregateLeavesUserJobs(aggKey.Public(), goodJob(), noSig), "missing the user signature")

	mismatched := goodJob()
	mismatched.SigningCommitments = &pbspark.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{"operator2": {}}}
	require.ErrorContains(t, validateAggregateLeavesUserJobs(aggKey.Public(), goodJob(), mismatched), "different operator commitment sets")
}

// TestSumKeyPackageForLeavesRejectsInconsistentKeyshares covers the cross-leaf
// guards. These matter more than their size suggests: sumOfSigningKeyshares
// iterates the first keyshare's operator set and treats a missing entry as the
// identity point, so a mismatched set would silently produce a wrong summed
// public share rather than an error, corrupting the aggregated signing key.
func TestSumKeyPackageForLeavesRejectsInconsistentKeyshares(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{21})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	handler := NewAggregateLeavesFlowHandler(&so.Config{Identifier: "operator1"})

	// Baseline: the fixture's leaves are consistent.
	_, _, err = handler.sumKeyPackageForLeaves(ctx, f.leaves)
	require.NoError(t, err)

	t.Run("mismatched min_signers rejected", func(t *testing.T) {
		_, err := dbClient.SigningKeyshare.UpdateOne(f.leafKeyshares[1]).SetMinSigners(3).Save(ctx)
		require.NoError(t, err)
		defer func() {
			_, err := dbClient.SigningKeyshare.UpdateOne(f.leafKeyshares[1]).SetMinSigners(2).Save(ctx)
			require.NoError(t, err)
		}()
		_, _, err = handler.sumKeyPackageForLeaves(ctx, f.leaves)
		require.ErrorContains(t, err, "min_signers")
	})

	t.Run("mismatched operator set size rejected", func(t *testing.T) {
		extra := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		_, err := dbClient.SigningKeyshare.UpdateOne(f.leafKeyshares[1]).
			SetPublicShares(map[string]keys.Public{"operator1": extra, "operator2": extra}).
			Save(ctx)
		require.NoError(t, err)
		_, _, err = handler.sumKeyPackageForLeaves(ctx, f.leaves)
		require.ErrorContains(t, err, "operator set size")
	})

	t.Run("missing public share for an operator rejected", func(t *testing.T) {
		other := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		_, err := dbClient.SigningKeyshare.UpdateOne(f.leafKeyshares[1]).
			SetPublicShares(map[string]keys.Public{"operator2": other}).
			Save(ctx)
		require.NoError(t, err)
		_, _, err = handler.sumKeyPackageForLeaves(ctx, f.leaves)
		require.ErrorContains(t, err, "missing a public share")
	})
}

// TestResolveCommitInputsRefreshesFromLockedState covers the coordinator side
// of the flow: BuildCommitPayload must derive everything it signs against from
// one locked read rather than from the snapshot the entrypoint took before the
// engine ran. The flow is seeded with a deliberately wrong cached subtree,
// aggregated key, and transaction set, and all three must be replaced.
func TestResolveCommitInputsRefreshesFromLockedState(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{22})
	f := createAggregateLeavesFixture(t, ctx, rng)
	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)

	// A second, unrelated subtree stands in for a stale entrypoint snapshot.
	other := createAggregateLeavesFixture(t, ctx, rand.NewChaCha8([32]byte{23}))
	stale := &aggregateLeavesSubtree{target: other.target, leaves: other.leaves}
	staleKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	flow := &aggregateLeavesCoordinatorFlow{
		AggregateLeavesFlowHandler: NewAggregateLeavesFlowHandler(&so.Config{Identifier: "operator1"}),
		prepareOp: &pbinternal.AggregateLeavesPrepareRequest{
			TargetNodeId:                 f.target.ID.String(),
			LeafIds:                      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
			OwnerIdentityPublicKey:       f.ownerIdentity.Public().Serialize(),
			RefundTxSigningJob:           &pbspark.UserSignedTxSigningJob{RawTx: refundRaw},
			WatchtowerRefundTxSigningJob: &pbspark.UserSignedTxSigningJob{RawTx: watchtowerRaw},
		},
		subtree:           stale,
		aggregatedUserKey: staleKey,
		txs:               nil,
	}

	require.NoError(t, flow.resolveCommitInputs(ctx))

	assert.Equal(t, f.target.ID, flow.subtree.target.ID, "subtree must come from the prepare op, not the cached snapshot")
	assert.NotEqual(t, other.target.ID, flow.subtree.target.ID)
	require.Len(t, flow.subtree.leaves, 2)
	assert.Equal(t, f.leaves[0].ID, flow.subtree.leaves[0].ID)
	assert.True(t, flow.aggregatedUserKey.Equals(f.aggregatedUserKey()),
		"aggregated key must be re-derived from the reloaded leaves")
	assert.False(t, flow.aggregatedUserKey.Equals(staleKey))

	// The transactions must be rebuilt against the reloaded target's prevout,
	// not left as whatever the entrypoint cached.
	require.NotNil(t, flow.txs)
	assert.Equal(t, f.targetValue, flow.txs.PrevOut.Value)
	assert.Equal(t, f.targetValue, flow.txs.RefundTx.TxOut[0].Value)
	assert.Equal(t, common.MaybeApplyFee(f.targetValue), flow.txs.WatchtowerRefundTx.TxOut[0].Value)
	parentTxHash := f.parentTx.TxHash()
	assert.Equal(t, parentTxHash, flow.txs.RefundTx.TxIn[0].PreviousOutPoint.Hash)
	assert.Equal(t, parentTxHash, flow.txs.WatchtowerRefundTx.TxIn[0].PreviousOutPoint.Hash)
}

// TestResolveCommitInputsRejectsMutatedLeafKeys pins that the refresh is a
// revalidation, not just a reload: if a leaf's key material no longer sums to
// the target's verifying key, the commit build must fail rather than sign.
func TestResolveCommitInputsRejectsMutatedLeafKeys(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{24})
	f := createAggregateLeavesFixture(t, ctx, rng)
	refundRaw, watchtowerRaw := buildAggregateLeavesUserTxs(t, f)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	flow := &aggregateLeavesCoordinatorFlow{
		AggregateLeavesFlowHandler: NewAggregateLeavesFlowHandler(&so.Config{Identifier: "operator1"}),
		prepareOp: &pbinternal.AggregateLeavesPrepareRequest{
			TargetNodeId:                 f.target.ID.String(),
			LeafIds:                      []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
			OwnerIdentityPublicKey:       f.ownerIdentity.Public().Serialize(),
			RefundTxSigningJob:           &pbspark.UserSignedTxSigningJob{RawTx: refundRaw},
			WatchtowerRefundTxSigningJob: &pbspark.UserSignedTxSigningJob{RawTx: watchtowerRaw},
		},
	}
	// Move a leaf's owner signing key so the aggregated key no longer matches
	// the transactions the user signed.
	_, err = dbClient.TreeNode.UpdateOne(f.leaves[0]).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		Save(ctx)
	require.NoError(t, err)

	err = flow.resolveCommitInputs(ctx)
	require.Error(t, err, "a leaf key change must fail the commit build rather than sign a mismatched package")
	require.ErrorContains(t, err, "user transaction validation failed")
}
