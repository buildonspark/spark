package handler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

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

// newQueryCountingSQLiteContext builds the same test context as
// db.NewTestSQLiteContext, but with a driver wrapper that records every
// SELECT so tests can assert on query counts.
func newQueryCountingSQLiteContext(t *testing.T) (context.Context, *queryCountingDriver) {
	t.Helper()
	drv, err := entsql.Open(dialect.SQLite, fmt.Sprintf("file:%s?mode=memory&_fk=1", strings.ReplaceAll(t.Name(), "/", "_")))
	require.NoError(t, err)
	// A single connection keeps the in-memory database visible to both the
	// schema migration and the session transaction.
	drv.DB().SetMaxOpenConns(1)
	counting := &queryCountingDriver{Driver: drv}
	client := ent.NewClient(ent.Driver(counting))
	require.NoError(t, client.Schema.Create(t.Context()))

	session := db.NewDefaultSessionFactory(client).NewSession(t.Context())
	t.Cleanup(func() {
		if tx := session.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
		_ = client.Close()
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
	ctx, counter := newQueryCountingSQLiteContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 3)
	sigs := nodeSignaturesForLeaves(leaves)

	counter.Reset()
	sparkNodes, internalNodes, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	require.Len(t, sparkNodes, 3)
	require.Len(t, internalNodes, 3)

	// Budget: one batched node load plus one query per eager-loaded edge type
	// (children, parent, tree, signing keyshare), plus two per node that are
	// inherent to the UpdateOne mutation itself: ent's internal post-UPDATE
	// refetch in Save, and the AVAILABLE-transition guard hook on TreeNode.
	// Anything above this means per-node loads crept back in.
	selects := counter.Selects()
	assert.LessOrEqual(t, len(selects), 5+2*len(sigs),
		"finalize must not issue per-node SELECTs beyond the UpdateOne refetch; got %d selects:\n%s",
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
	ctx, counter := newQueryCountingSQLiteContext(t)
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
// ~9 SELECTs per leaf. The marginal cost of one extra node must be exactly
// the two selects inherent to its UpdateOne mutation (ent's post-UPDATE
// refetch and the AVAILABLE-transition guard hook) — everything else has to
// come from the batched load. If this fails after adding or removing a
// TreeNode mutation hook that queries, that's expected: update the constant
// here and confirm the budget test above still holds.
func TestUpdateNodesFromSignaturesMarginalCostIsTwoSelectsPerNode(t *testing.T) {
	var small, large int
	t.Run("n=1", func(t *testing.T) { small = measureFinalizeSelects(t, 1) })
	t.Run("n=50", func(t *testing.T) { large = measureFinalizeSelects(t, 50) })

	assert.Equal(t, 2*49, large-small,
		"each additional node must add exactly 2 SELECTs (UpdateOne refetch + AVAILABLE guard); got %d for 49 extra nodes", large-small)
}

func TestUpdateNodesFromSignaturesRejectsInvalidAndMissingNodes(t *testing.T) {
	ctx, _ := newQueryCountingSQLiteContext(t)
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
	ctx, _ := newQueryCountingSQLiteContext(t)
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
	ctx, _ := newQueryCountingSQLiteContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 1)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	bare, err := dbTx.TreeNode.Get(ctx, leaves[0].ID)
	require.NoError(t, err)

	sparkNode, internalNode, err := handler.updateLoadedNode(ctx, &pb.NodeSignatures{NodeId: bare.ID.String()}, bare, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	require.NotNil(t, sparkNode)
	require.NotNil(t, internalNode)

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
	ctx, _ := newQueryCountingSQLiteContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 1)
	sigs := nodeSignaturesForLeaves([]*ent.TreeNode{leaves[0], leaves[0]})

	_, _, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, false)
	require.ErrorContains(t, err, "duplicate node id")
}

func TestUpdateNodesFromSignaturesWrapsErrorsWithNodeID(t *testing.T) {
	ctx, _ := newQueryCountingSQLiteContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{})
	leaves := createFinalizeFixture(t, ctx, 2)
	sigs := nodeSignaturesForLeaves(leaves)
	// Garbage refund signature makes the second entry fail mid-batch; the
	// error must identify which node failed.
	sigs[1].RefundTxSignature = []byte{0x01}

	_, _, err := handler.updateNodesFromSignatures(ctx, sigs, pbcommon.SignatureIntent_TRANSFER, false)
	require.ErrorContains(t, err, fmt.Sprintf("failed to update node %s", leaves[1].ID))
}
