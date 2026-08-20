package task

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
)

// abandonedTreeFixture builds the shape the sweep hunts: a PENDING tree over an
// unconfirmed deposit address with a CREATING root and children. Knobs poke
// holes in the predicate one dimension at a time.
type abandonedTreeFixture struct {
	tree     *ent.Tree
	root     *ent.TreeNode
	children []*ent.TreeNode
}

func createAbandonedTreeFixture(
	t *testing.T,
	ctx context.Context,
	rng *rand.ChaCha8,
	client *ent.Client,
	confirmationHeight int64,
	withDepositAddress bool,
	childCount int,
	nodeCreateTime time.Time,
	treeCreateTime time.Time,
) abandonedTreeFixture {
	t.Helper()

	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createSenderInitiatedExpirySigningKeyshare(t, ctx, rng, client)

	treeCreate := client.Tree.Create().
		SetStatus(st.TreeStatusPending).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(owner).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetCreateTime(treeCreateTime)

	if withDepositAddress {
		depositAddressCreate := client.DepositAddress.Create().
			SetAddress(fmt.Sprintf("retire_test_%x", keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()[:12])).
			SetOwnerIdentityPubkey(owner).
			SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(keyshare)
		if confirmationHeight != 0 {
			depositAddressCreate.SetConfirmationHeight(confirmationHeight)
		}
		depositAddress, err := depositAddressCreate.Save(ctx)
		require.NoError(t, err)
		treeCreate.SetDepositAddress(depositAddress)
	}

	fixtureTree, err := treeCreate.Save(ctx)
	require.NoError(t, err)

	root := createRetireFixtureLeaf(t, ctx, rng, client, fixtureTree, keyshare, nil, nodeCreateTime)
	children := make([]*ent.TreeNode, 0, childCount)
	for range childCount {
		children = append(children,
			createRetireFixtureLeaf(t, ctx, rng, client, fixtureTree, keyshare, root, nodeCreateTime))
	}
	return abandonedTreeFixture{tree: fixtureTree, root: root, children: children}
}

func createRetireFixtureLeaf(
	t *testing.T,
	ctx context.Context,
	rng *rand.ChaCha8,
	client *ent.Client,
	fixtureTree *ent.Tree,
	keyshare *ent.SigningKeyshare,
	parent *ent.TreeNode,
	createTime time.Time,
) *ent.TreeNode {
	t.Helper()

	create := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusCreating).
		SetTree(fixtureTree).
		SetNetwork(fixtureTree.Network).
		SetSigningKeyshare(keyshare).
		SetValue(1000).
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(fixtureTree.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(senderInitiatedExpiryRawTxBytes(t, 1)).
		SetRawRefundTx(senderInitiatedExpiryRawTxBytes(t, 2)).
		SetDirectTx(senderInitiatedExpiryRawTxBytes(t, 1)).
		SetDirectRefundTx(senderInitiatedExpiryRawTxBytes(t, 3)).
		SetDirectFromCpfpRefundTx(senderInitiatedExpiryRawTxBytes(t, 4)).
		SetVout(0).
		SetCreateTime(createTime)
	if parent != nil {
		create = create.SetParent(parent)
	}
	leaf, err := create.Save(ctx)
	require.NoError(t, err)
	return leaf
}

func assertTreeState(t *testing.T, ctx context.Context, client *ent.Client, treeID uuid.UUID, expectedTreeStatus st.TreeStatus, expectedNodeStatus st.TreeNodeStatus) {
	t.Helper()
	refreshedTree, err := client.Tree.Query().Where(tree.ID(treeID)).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, expectedTreeStatus, refreshedTree.Status)

	nodes, err := client.TreeNode.Query().Where(treenode.HasTreeWith(tree.ID(refreshedTree.ID))).All(ctx)
	require.NoError(t, err)
	for _, node := range nodes {
		assert.Equal(t, expectedNodeStatus, node.Status, "node %s", node.ID)
	}
}

func TestRetireAbandonedPendingTrees_RetiresDeadTree(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{101})

	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 2, old, old)

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	assertTreeState(t, ctx, client, fixture.tree.ID, st.TreeStatusCreationAbandoned, st.TreeNodeStatusCreationAbandoned)
}

func TestRetireAbandonedPendingTrees_SkipsLoneRoot(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{102})

	// A lone CREATING root is a user deposit whose funding tx may still be
	// broadcast later; the sweep must never touch it.
	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 0, old, old)

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	assertTreeState(t, ctx, client, fixture.tree.ID, st.TreeStatusPending, st.TreeNodeStatusCreating)
}

func TestRetireAbandonedPendingTrees_SkipsYoungTree(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{103})

	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 2, time.Now(), old)

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	assertTreeState(t, ctx, client, fixture.tree.ID, st.TreeStatusPending, st.TreeNodeStatusCreating)
}

func TestRetireAbandonedPendingTrees_SkipsTreeWithAdvancedNode(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{104})

	// Any node past CREATING means signatures landed or the watcher advanced
	// something — the tree is not provably dead.
	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 2, old, old)
	require.NoError(t, client.TreeNode.UpdateOne(fixture.children[0]).
		SetStatus(st.TreeNodeStatusAvailable).Exec(ctx))

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	refreshedTree, err := client.Tree.Get(ctx, fixture.tree.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeStatusPending, refreshedTree.Status)
	refreshedRoot, err := client.TreeNode.Get(ctx, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusCreating, refreshedRoot.Status)
}

func TestRetireAbandonedPendingTrees_SkipsConfirmedDepositAddress(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{105})

	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 857_000, true, 2, old, old)

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	assertTreeState(t, ctx, client, fixture.tree.ID, st.TreeStatusPending, st.TreeNodeStatusCreating)
}

func TestRetireAbandonedPendingTrees_SkipsTreeWithoutDepositAddress(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{106})

	// Retirement requires positive evidence the funding is dead; with no
	// deposit address edge there is nothing to verify against.
	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 0, false, 2, old, old)

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	assertTreeState(t, ctx, client, fixture.tree.ID, st.TreeStatusPending, st.TreeNodeStatusCreating)
}

func TestRetireAbandonedPendingTrees_SkipsWhenBaseTxidSeenOnChain(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{107})

	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 2, old, old)

	// The chain watcher stores utxos.txid in display byte order, which is
	// what the sweep must match against.
	displayOrderTxid, err := hex.DecodeString(fixture.tree.BaseTxid.String())
	require.NoError(t, err)
	depositAddress, err := fixture.tree.QueryDepositAddress().Only(ctx)
	require.NoError(t, err)
	_, err = client.Utxo.Create().
		SetTxid(displayOrderTxid).
		SetVout(0).
		SetAmount(10_000).
		SetPkScript([]byte{0x51, 0x20}).
		SetNetwork(btcnetwork.Regtest).
		SetBlockHeight(857_001).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	assertTreeState(t, ctx, client, fixture.tree.ID, st.TreeStatusPending, st.TreeNodeStatusCreating)
}

// The two tests below drive retireOneAbandonedTree directly: the branches
// under test detect state changing between candidate selection and the
// per-tree locks, which the task boundary cannot reach — a fixture either
// qualifies at selection (and stays qualified) or is filtered out before the
// in-transaction recheck ever runs.
func TestRetireOneAbandonedTree_SkipsTreeThatVanishedAfterSelection(t *testing.T) {
	t.Parallel()
	ctx, _ := db.ConnectToTestPostgres(t)

	nodesRetired, err := retireOneAbandonedTree(ctx, uuid.New(), time.Now().Add(-retireAbandonedTreeMinAge))
	require.NoError(t, err)
	assert.Zero(t, nodesRetired)
}

func TestRetireOneAbandonedTree_SkipsTreeThatAdvancedAfterSelection(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{110})

	old := time.Now().Add(-4 * 24 * time.Hour)
	fixture := createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 2, old, old)
	// The tree qualified at selection; a node then advanced before the
	// per-tree transaction took its locks.
	require.NoError(t, client.TreeNode.UpdateOne(fixture.children[0]).
		SetStatus(st.TreeNodeStatusAvailable).Exec(ctx))

	nodesRetired, err := retireOneAbandonedTree(ctx, fixture.tree.ID, time.Now().Add(-retireAbandonedTreeMinAge))
	require.NoError(t, err)
	assert.Zero(t, nodesRetired)

	refreshedTree, err := client.Tree.Get(ctx, fixture.tree.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeStatusPending, refreshedTree.Status)
	refreshedRoot, err := client.TreeNode.Get(ctx, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusCreating, refreshedRoot.Status)
}

func TestRetireAbandonedPendingTrees_PermanentSkipsDoNotStarveTheCap(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{109})

	// Prod holds dozens of ancient PENDING trees with no deposit address edge,
	// which the funding-evidence check skips forever. If they counted as
	// candidates, oldest-first selection would hand them every slot on every
	// run and the sweep would never retire anything.
	old := time.Now().Add(-300 * 24 * time.Hour)
	skippers := make([]abandonedTreeFixture, 0, retireAbandonedTreesPerRun+1)
	for range retireAbandonedTreesPerRun + 1 {
		skippers = append(skippers,
			createAbandonedTreeFixture(t, ctx, rng, client, 0, false, 1, old, old))
	}
	newerButDead := time.Now().Add(-4 * 24 * time.Hour)
	retirable := createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 1, newerButDead, newerButDead)

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	assertTreeState(t, ctx, client, retirable.tree.ID, st.TreeStatusCreationAbandoned, st.TreeNodeStatusCreationAbandoned)
	for _, skipper := range skippers {
		refreshedTree, err := client.Tree.Get(ctx, skipper.tree.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeStatusPending, refreshedTree.Status)
	}
}

func TestRetireAbandonedPendingTrees_RespectsPerRunCapOldestFirst(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	rng := rand.NewChaCha8([32]byte{108})

	fixtures := make([]abandonedTreeFixture, 0, retireAbandonedTreesPerRun+2)
	base := time.Now().Add(-30 * 24 * time.Hour)
	for i := range retireAbandonedTreesPerRun + 2 {
		createdAt := base.Add(time.Duration(i) * 24 * time.Hour)
		fixtures = append(fixtures,
			createAbandonedTreeFixture(t, ctx, rng, client, 0, true, 1, createdAt, createdAt))
	}

	require.NoError(t, retireAbandonedPendingTrees(ctx))

	for i, fixture := range fixtures {
		refreshedTree, err := client.Tree.Get(ctx, fixture.tree.ID)
		require.NoError(t, err)
		if i < retireAbandonedTreesPerRun {
			assert.Equal(t, st.TreeStatusCreationAbandoned, refreshedTree.Status, "tree %d should be retired", i)
		} else {
			assert.Equal(t, st.TreeStatusPending, refreshedTree.Status, "tree %d is past the cap", i)
		}
	}
}
