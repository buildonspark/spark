package handler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the query-batching contract of the finalize path: the
// consensus claim commit applies signatures to hundreds of leaves inside one
// request tx, and per-leaf SELECTs there were the dominant cost of large
// claims in production. The contract is invisible at the application
// boundary — a public-API test passes with or without the N+1 — so this is a
// narrow lower-level test against the batch seam shared by
// FinalizeNodeSignatures and the consensus claim commit.

// queryCountingDriver wraps a dialect.Driver and records every SQL statement
// issued through Query (SELECTs), both directly and inside transactions.
type queryCountingDriver struct {
	dialect.Driver
	mu      sync.Mutex
	queries []string
}

func (d *queryCountingDriver) record(query string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = append(d.queries, query)
}

func (d *queryCountingDriver) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = nil
}

func (d *queryCountingDriver) Selects() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var selects []string
	for _, q := range d.queries {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q)), "SELECT") {
			selects = append(selects, q)
		}
	}
	return selects
}

func (d *queryCountingDriver) Query(ctx context.Context, query string, args, v any) error {
	d.record(query)
	return d.Driver.Query(ctx, query, args, v)
}

func (d *queryCountingDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &queryCountingTx{Tx: tx, driver: d}, nil
}

type queryCountingTx struct {
	dialect.Tx
	driver *queryCountingDriver
}

func (t *queryCountingTx) Query(ctx context.Context, query string, args, v any) error {
	t.driver.record(query)
	return t.Tx.Query(ctx, query, args, v)
}

// newQueryCountingPostgresContext provisions a migrated test Postgres
// database (Postgres, not SQLite: the flush paths under test take FOR UPDATE
// row locks) and returns a context whose ent client records every SQL
// statement so tests can assert on query counts.
func newQueryCountingPostgresContext(t *testing.T) (context.Context, *queryCountingDriver) {
	t.Helper()
	_, tc := db.ConnectToTestPostgres(t)
	rawDB, err := tc.Client.RawDB()
	require.NoError(t, err)
	counting := &queryCountingDriver{Driver: entsql.OpenDB(dialect.Postgres, rawDB)}
	client := ent.NewClient(ent.Driver(counting))

	session := db.NewDefaultSessionFactory(client).NewSession(t.Context())
	// The underlying *sql.DB belongs to tc; its cleanup closes it.
	t.Cleanup(func() {
		if tx := session.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
	})
	return ent.Inject(t.Context(), session), counting
}

// createFinalizeFixture creates a tree with a parent node and n leaf children
// ready to be finalized with SignatureIntent_TRANSFER.
func createFinalizeFixture(t *testing.T, ctx context.Context, n int) []*ent.TreeNode {
	t.Helper()
	rng := rand.NewChaCha8([32]byte{42})
	tree, parentNode := createTestTree(t, ctx, btcnetwork.Regtest, st.TreeStatusAvailable)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	keyshare, err := parentNode.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)

	leaves := make([]*ent.TreeNode, 0, n)
	for i := range n {
		verifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerSigning := keys.MustGeneratePrivateKeyFromRand(rng)
		rawTx := createTestTxBytesWithIndex(t, 500, uint32(i))
		leaf, err := dbTx.TreeNode.Create().
			SetID(uuid.New()).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(keyshare).
			SetParent(parentNode).
			SetValue(500).
			SetVerifyingPubkey(verifyingKey.Public()).
			SetOwnerIdentityPubkey(ownerIdentity.Public()).
			SetOwnerSigningPubkey(ownerSigning.Public()).
			SetRawTx(rawTx).
			SetRawRefundTx(addDummyWitness(t, rawTx)).
			SetVout(0).
			SetStatus(st.TreeNodeStatusTransferLocked).
			Save(ctx)
		require.NoError(t, err)
		leaves = append(leaves, leaf)
	}
	return leaves
}

func nodeSignaturesForLeaves(leaves []*ent.TreeNode) []*pb.NodeSignatures {
	sigs := make([]*pb.NodeSignatures, 0, len(leaves))
	for _, leaf := range leaves {
		// No signatures provided: exercises the same status/marshal logic the
		// consensus claim commit uses without needing valid Schnorr signatures.
		sigs = append(sigs, &pb.NodeSignatures{NodeId: leaf.ID.String()})
	}
	return sigs
}

func TestUpdateNodesFromSignaturesDoesNotScaleSelectsWithNodeCount(t *testing.T) {
	ctx, counter := newQueryCountingPostgresContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 3)
	sigs := nodeSignaturesForLeaves(leaves)

	counter.Reset()
	sparkNodes, internalNodes, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	require.Len(t, sparkNodes, 3)
	require.Len(t, internalNodes, 3)

	// Budget: one batched node load plus one query per eager-loaded edge type
	// (children, parent, tree, signing keyshare), the row lock, and the
	// grouped status update's two queries (the mutation's ID resolution and
	// the AVAILABLE-transition guard's batched status check) — plus exactly
	// one query per node: the TreeNode create hook re-validating network
	// against the tree on each upsert row. Anything above that means more
	// per-node loads crept in.
	selects := counter.Selects()
	assert.LessOrEqual(t, len(selects), 8+len(sigs),
		"finalize must not issue per-node SELECTs beyond the network hook's; got %d selects:\n%s",
		len(selects), strings.Join(selects, "\n"))

	parentID := ""
	for i, leaf := range leaves {
		sparkNode := sparkNodes[i]
		require.Equal(t, leaf.ID.String(), sparkNode.GetId(), "results must align with request order")
		if parentID == "" {
			parent, err := leaf.QueryParent().Only(ctx)
			require.NoError(t, err)
			parentID = parent.ID.String()
		}
		assert.Equal(t, parentID, sparkNode.GetParentNodeId())
		assert.Equal(t, parentID, internalNodes[i].GetParentNodeId())
		assert.Equal(t, string(st.TreeNodeStatusAvailable), sparkNode.GetStatus())
	}
}

func measureFinalizeSelects(t *testing.T, n int) int {
	t.Helper()
	ctx, counter := newQueryCountingPostgresContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, n)
	sigs := nodeSignaturesForLeaves(leaves)

	counter.Reset()
	sparkNodes, _, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	require.Len(t, sparkNodes, n)
	return len(counter.Selects())
}

// The production incident this guards against: applying a large claim issued
// ~9 SELECTs per leaf. With the batched flush (one upsert for the byte
// fields, grouped status updates for the AVAILABLE-transition guard), an
// extra node costs exactly one additional SELECT: the TreeNode create hook's
// network re-validation on its upsert row. If this fails after adding a
// TreeNode mutation hook that queries per row, batch that hook instead of
// raising the constant.
func TestUpdateNodesFromSignaturesMarginalCostIsOneSelect(t *testing.T) {
	var small, large int
	t.Run("n=1", func(t *testing.T) { small = measureFinalizeSelects(t, 1) })
	t.Run("n=50", func(t *testing.T) { large = measureFinalizeSelects(t, 50) })

	assert.Equal(t, 49, large-small,
		"an additional node must add exactly the network hook's SELECT; got %d for 49 extra nodes", large-small)
}

// A second raw connection proves the flush's row lock is actually held: its
// FOR UPDATE NOWAIT on the same row must fail with lock_not_available while
// the request transaction is open.
func TestPersistFinalizedNodesLocksRowsOnPostgres(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	leaf := createDbLeaf(t, ctx, true)

	tree, err := leaf.node.QueryTree().Only(ctx)
	require.NoError(t, err)
	keyshare, err := leaf.node.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)
	node := leaf.node
	node.Edges.Tree = tree
	node.Edges.SigningKeyshare = keyshare
	node.UpdateTime = time.Now().UTC()

	// Commit the fixture so the row is visible to the second connection; the
	// flush below then runs (and holds its locks) in a fresh transaction.
	require.NoError(t, ent.DbCommit(ctx))

	require.NoError(t, persistFinalizedNodes(ctx, []*ent.TreeNode{node}, nil, node.UpdateTime))

	rawDB, err := sessionCtx.Client.RawDB()
	require.NoError(t, err)
	conn, err := rawDB.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(t.Context(), "BEGIN")
	require.NoError(t, err)
	defer func() { _, _ = conn.ExecContext(t.Context(), "ROLLBACK") }()
	var lockedID string
	err = conn.QueryRowContext(t.Context(),
		"SELECT id FROM tree_nodes WHERE id = $1::uuid FOR UPDATE NOWAIT", node.ID.String()).Scan(&lockedID)
	require.ErrorContains(t, err, "could not obtain lock")

	ghost := &ent.TreeNode{ID: uuid.New()}
	err = persistFinalizedNodes(ctx, []*ent.TreeNode{ghost}, nil, node.UpdateTime)
	require.ErrorContains(t, err, "expected 1 tree nodes to finalize but locked 0")
}

// A terminal transition committed between finalize's unlocked read and its
// flush — e.g. the chain watcher exiting a leaf to L1 — must fail the whole
// request for any stale target status, not only AVAILABLE (the schema
// guard's fence), or a stale SPLITTED would overwrite the exit.
func TestPersistFinalizedNodesRejectsStaleTransitionsOverTerminalStatus(t *testing.T) {
	for _, target := range []st.TreeNodeStatus{st.TreeNodeStatusAvailable, st.TreeNodeStatusSplitted} {
		t.Run(string(target), func(t *testing.T) {
			ctx, _ := newQueryCountingPostgresContext(t)
			leaves := createFinalizeFixture(t, ctx, 1)

			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)
			_, err = dbTx.TreeNode.UpdateOneID(leaves[0].ID).SetStatus(st.TreeNodeStatusExited).Save(ctx)
			require.NoError(t, err)

			err = persistFinalizedNodes(ctx, nil, map[st.TreeNodeStatus][]uuid.UUID{
				target: {leaves[0].ID},
			}, time.Now().UTC())
			require.ErrorContains(t, err, "in terminal status EXITED cannot transition to")
		})
	}
}

func measureOwnerKeyUpdateSelects(t *testing.T, n int) int {
	t.Helper()
	ctx, counter := newQueryCountingPostgresContext(t)
	leaves := createFinalizeFixture(t, ctx, n)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updates := make([]treeNodeOwnerKeyUpdate, 0, n)
	for _, leaf := range leaves {
		node, err := dbTx.TreeNode.Query().
			Where(enttreenode.IDEQ(leaf.ID)).
			WithTree().
			WithSigningKeyshare().
			Only(ctx)
		require.NoError(t, err)
		updates = append(updates, treeNodeOwnerKeyUpdate{
			node:                node,
			ownerSigningPubkey:  node.OwnerSigningPubkey,
			ownerIdentityPubkey: node.OwnerIdentityPubkey,
		})
	}

	counter.Reset()
	require.NoError(t, applyTreeNodeOwnerKeyUpdates(ctx, dbTx, uuid.New(), updates, true))
	return len(counter.Selects())
}

// An additional node costs the owner-key flush exactly one SELECT: the
// TreeNode create hook's network re-validation on its upsert row. Everything
// else (lock, rotation, upserts) is flat.
func TestApplyTreeNodeOwnerKeyUpdatesMarginalCostIsOneSelect(t *testing.T) {
	var small, large int
	t.Run("n=1", func(t *testing.T) { small = measureOwnerKeyUpdateSelects(t, 1) })
	t.Run("n=50", func(t *testing.T) { large = measureOwnerKeyUpdateSelects(t, 50) })

	assert.Equal(t, 49, large-small,
		"an additional node must add exactly the network hook's SELECT; got %d for 49 extra nodes", large-small)
}

// Redelivered finalizes rewrite the same status; the terminal re-check must
// not break that idempotency.
func TestPersistFinalizedNodesAllowsSameStatusRewrite(t *testing.T) {
	ctx, _ := newQueryCountingPostgresContext(t)
	leaves := createFinalizeFixture(t, ctx, 1)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	_, err = dbTx.TreeNode.UpdateOneID(leaves[0].ID).SetStatus(st.TreeNodeStatusSplitted).Save(ctx)
	require.NoError(t, err)

	require.NoError(t, persistFinalizedNodes(ctx, nil, map[st.TreeNodeStatus][]uuid.UUID{
		st.TreeNodeStatusSplitted: {leaves[0].ID},
	}, time.Now().UTC()))
}

func TestUpdateNodesFromSignaturesRejectsInvalidAndMissingNodes(t *testing.T) {
	ctx, _ := newQueryCountingPostgresContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})

	_, _, err := handler.updateNodesFromSignatures(ctx, []*pb.NodeSignatures{{NodeId: "not-a-uuid"}}, pbcommon.SignatureIntent_TRANSFER, false)
	require.ErrorContains(t, err, "invalid node id")

	_, _, err = handler.updateNodesFromSignatures(ctx, []*pb.NodeSignatures{{NodeId: uuid.New().String()}}, pbcommon.SignatureIntent_TRANSFER, false)
	require.ErrorContains(t, err, "failed to get node")
}

type claimRefundSigs struct {
	cpfp           []byte
	direct         []byte
	directFromCpfp []byte
}

// createClaimStyleLeaf creates a leaf whose node/direct txs pay to a key we
// control, with unsigned refund variants stored on the row and valid schnorr
// signatures for all three — the shape applyClaimTransferCommit finalizes.
func createClaimStyleLeaf(t *testing.T, ctx context.Context, tree *ent.Tree, parent *ent.TreeNode, keyshare *ent.SigningKeyshare, i int) (*ent.TreeNode, claimRefundSigs) {
	t.Helper()
	rng := rand.NewChaCha8([32]byte{byte(100 + i)})
	ownerKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	pkScript, err := common.P2TRScriptFromPubKey(ownerKey.Public())
	require.NoError(t, err)

	newTx := func(prevHash chainhash.Hash, prevIndex uint32, value int64) *wire.MsgTx {
		tx := wire.NewMsgTx(3)
		tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: prevHash, Index: prevIndex}, nil, nil))
		tx.AddTxOut(wire.NewTxOut(value, pkScript))
		return tx
	}
	fundingHash := chainhash.Hash{byte(i + 1), 0xfe}
	nodeTx := newTx(fundingHash, 0, 1000)
	directTx := newTx(fundingHash, 1, 1000)
	cpfpRefundTx := newTx(nodeTx.TxHash(), 0, 900)
	directRefundTx := newTx(directTx.TxHash(), 0, 900)
	directFromCpfpRefundTx := newTx(nodeTx.TxHash(), 0, 800)

	signFor := func(tx *wire.MsgTx, prevOut *wire.TxOut) []byte {
		prevFetcher := txscript.NewCannedPrevOutputFetcher(prevOut.PkScript, prevOut.Value)
		hashes := txscript.NewTxSigHashes(tx, prevFetcher)
		sigHash, err := txscript.CalcTaprootSignatureHash(hashes, txscript.SigHashDefault, tx, 0, prevFetcher)
		require.NoError(t, err)
		taprootKey := txscript.TweakTaprootPrivKey(*ownerKey.ToBTCEC(), []byte{})
		sig, err := schnorr.Sign(taprootKey, sigHash)
		require.NoError(t, err)
		return sig.Serialize()
	}
	sigs := claimRefundSigs{
		cpfp:           signFor(cpfpRefundTx, nodeTx.TxOut[0]),
		direct:         signFor(directRefundTx, directTx.TxOut[0]),
		directFromCpfp: signFor(directFromCpfpRefundTx, nodeTx.TxOut[0]),
	}

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	leaf, err := dbTx.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(keyshare).
		SetParent(parent).
		SetValue(1000).
		SetVerifyingPubkey(verifyingKey.Public()).
		SetOwnerIdentityPubkey(ownerIdentity.Public()).
		SetOwnerSigningPubkey(ownerKey.Public()).
		SetRawTx(serializeTx(t, nodeTx)).
		SetDirectTx(serializeTx(t, directTx)).
		SetRawRefundTx(serializeTx(t, cpfpRefundTx)).
		SetDirectRefundTx(serializeTx(t, directRefundTx)).
		SetDirectFromCpfpRefundTx(serializeTx(t, directFromCpfpRefundTx)).
		SetVout(0).
		SetStatus(st.TreeNodeStatusTransferLocked).
		Save(ctx)
	require.NoError(t, err)
	return leaf, sigs
}

// Mirrors the consensus claim commit's caller shape — TRANSFER intent,
// requireDirectTx=true, empty node-tx signatures, all three refund signature
// variants — across multiple leaves, and verifies the signatures are applied
// and persisted.
func TestUpdateNodesFromSignaturesAppliesAllRefundVariantsWithDirectTxRequired(t *testing.T) {
	ctx, _ := newQueryCountingPostgresContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	tree, parentNode := createTestTree(t, ctx, btcnetwork.Regtest, st.TreeStatusAvailable)
	keyshare, err := parentNode.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)

	var sigs []*pb.NodeSignatures
	var leaves []*ent.TreeNode
	for i := range 2 {
		leaf, refundSigs := createClaimStyleLeaf(t, ctx, tree, parentNode, keyshare, i)
		leaves = append(leaves, leaf)
		sigs = append(sigs, &pb.NodeSignatures{
			NodeId:                          leaf.ID.String(),
			NodeTxSignature:                 []byte{},
			DirectNodeTxSignature:           []byte{},
			RefundTxSignature:               refundSigs.cpfp,
			DirectRefundTxSignature:         refundSigs.direct,
			DirectFromCpfpRefundTxSignature: refundSigs.directFromCpfp,
		})
	}

	sparkNodes, _, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, true)
	require.NoError(t, err)
	require.Len(t, sparkNodes, 2)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	for i, leaf := range leaves {
		saved, err := dbTx.TreeNode.Get(ctx, leaf.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TreeNodeStatusAvailable, saved.Status)
		for name, rawTx := range map[string][]byte{
			"refund":           saved.RawRefundTx,
			"direct refund":    saved.DirectRefundTx,
			"direct-from-cpfp": saved.DirectFromCpfpRefundTx,
		} {
			tx, err := common.TxFromRawTxBytes(rawTx)
			require.NoError(t, err)
			assert.NotEmpty(t, tx.TxIn[0].Witness, "%s tx of leaf %d must carry the applied signature", name, i)
		}
		assert.Equal(t, string(st.TreeNodeStatusAvailable), sparkNodes[i].GetStatus())
	}
}

// updateLoadedNode's edge fallbacks are unreachable through
// updateNodesFromSignatures (loadNodesForFinalize always eager-loads every
// edge), so exercise the defense-in-depth branches directly with a bare node
// and confirm they reproduce the preloaded behavior.
func TestUpdateLoadedNodeFallsBackToQueriesWhenEdgesNotLoaded(t *testing.T) {
	ctx, _ := newQueryCountingPostgresContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 1)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	bare, err := dbTx.TreeNode.Get(ctx, leaves[0].ID)
	require.NoError(t, err)

	newStatus, err := handler.updateLoadedNode(ctx, &pb.NodeSignatures{NodeId: bare.ID.String()}, bare, pbcommon.SignatureIntent_TRANSFER, false, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, newStatus)
	assert.Equal(t, st.TreeNodeStatusAvailable, *newStatus)
	assert.Equal(t, st.TreeNodeStatusAvailable, bare.Status)

	// The fallback queries populate the edges, so marshaling reproduces the
	// preloaded behavior.
	sparkNode, err := bare.MarshalSparkProto(ctx)
	require.NoError(t, err)
	internalNode, err := bare.MarshalInternalProto(ctx)
	require.NoError(t, err)

	parent, err := leaves[0].QueryParent().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, parent.ID.String(), sparkNode.GetParentNodeId())
	assert.Equal(t, parent.ID.String(), internalNode.GetParentNodeId())
	assert.Equal(t, string(st.TreeNodeStatusAvailable), sparkNode.GetStatus())
}

// Duplicate IDs must be rejected at the batching seam itself, not just by the
// callers: the batch loader aliases duplicates to one *ent.TreeNode, so a
// second occurrence would be applied against a stale pre-update snapshot
// instead of the fresh read the old per-node code did. All production callers
// already reject duplicates upstream; this pins the defense in depth.
func TestUpdateNodesFromSignaturesRejectsDuplicateNodeIDs(t *testing.T) {
	ctx, _ := newQueryCountingPostgresContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 1)
	sigs := nodeSignaturesForLeaves([]*ent.TreeNode{leaves[0], leaves[0]})

	_, _, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, false)
	require.ErrorContains(t, err, "duplicate node id")
}

func TestUpdateNodesFromSignaturesWrapsErrorsWithNodeID(t *testing.T) {
	ctx, _ := newQueryCountingPostgresContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 2)
	sigs := nodeSignaturesForLeaves(leaves)
	// Garbage refund signature makes the second entry fail mid-batch; the
	// error must identify which node failed.
	sigs[1].RefundTxSignature = []byte{0x01}

	_, _, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, false)
	require.ErrorContains(t, err, fmt.Sprintf("failed to update node %s", leaves[1].ID))
}
