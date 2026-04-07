package handler

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/knobs"
)

// --- Helpers for constructing minimal valid transactions and DB state ---

const (
	testTimeLock    = 1000
	testSourceValue = 100000
)

func serializeTx(t *testing.T, tx *wire.MsgTx) []byte {
	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func newTestTx(value int64, sequence uint32, prevTxHash *chainhash.Hash, pkScript []byte) *wire.MsgTx {
	tx := wire.NewMsgTx(3)
	if prevTxHash == nil {
		prevTxHash = &chainhash.Hash{}
	}
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: *prevTxHash, Index: 0},
		Sequence:         sequence,
	})
	tx.AddTxOut(&wire.TxOut{Value: value, PkScript: pkScript})
	return tx
}

type testLeaf struct {
	node *ent.TreeNode
	// Cached values
	nodeTxHash   chainhash.Hash
	directTxHash chainhash.Hash
}

type testConnector struct {
	raw    []byte
	txHash chainhash.Hash
}

func createDbLeaf(t *testing.T, ctx context.Context, requireNodeTxTimelock bool) *testLeaf {
	t.Helper()
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Minimal tree and keyshare
	tree, err := tx.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		Save(ctx)
	require.NoError(t, err)

	secret := keys.GeneratePrivateKey()
	ks, err := tx.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"1": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	srcScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	nodeSeq := uint32(0)
	directSeq := spark.DirectTimelockOffset
	if requireNodeTxTimelock {
		nodeSeq = spark.TimeLockInterval
		directSeq = nodeSeq + spark.DirectTimelockOffset
	}

	nodeTx := newTestTx(testSourceValue, nodeSeq, nil, srcScript)
	nodeTxHash := nodeTx.TxHash()
	directTx := newTestTx(testSourceValue, directSeq, nil, srcScript)
	directTxHash := directTx.TxHash()
	// Existing CPFP refund tx in DB with timelock = testTimeLock
	cpfpRefund := newTestTx(testSourceValue, testTimeLock, &nodeTxHash, srcScript)

	node, err := tx.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(ks).
		SetValue(testSourceValue).
		SetVerifyingPubkey(secret.Public()).
		SetOwnerIdentityPubkey(secret.Public()).
		SetOwnerSigningPubkey(secret.Public()).
		SetVout(0).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeNodeStatusAvailable).
		SetRawTx(serializeTx(t, nodeTx)).
		SetDirectTx(serializeTx(t, directTx)).
		SetRawRefundTx(serializeTx(t, cpfpRefund)).
		Save(ctx)
	require.NoError(t, err)

	return &testLeaf{
		node:         node,
		nodeTxHash:   nodeTxHash,
		directTxHash: directTxHash,
	}
}

func makeClientCpfpTx(t *testing.T, leaf *testLeaf, refundDest keys.Public) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	expectedCpfp := uint32(testTimeLock - spark.TimeLockInterval)
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		Sequence:         expectedCpfp,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	return serializeTx(t, tx)
}

func makeClientDirectTx(t *testing.T, leaf *testLeaf, refundDest keys.Public) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	expected := testTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.directTxHash, Index: 0},
		Sequence:         expected,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	return serializeTx(t, tx)
}

func makeClientDirectFromCpfpTx(t *testing.T, leaf *testLeaf, refundDest keys.Public) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	expected := testTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		Sequence:         expected,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	return serializeTx(t, tx)
}

func makeClientCoopExitCpfpTx(t *testing.T, leaf *testLeaf, refundDest keys.Public, connector *testConnector) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)

	expectedCpfp := uint32(testTimeLock - spark.TimeLockInterval)
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		Sequence:         expectedCpfp,
	})
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: connector.txHash, Index: 0},
		Sequence:         0,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	return serializeTx(t, tx)
}

func makeClientCoopExitDirectTx(t *testing.T, leaf *testLeaf, refundDest keys.Public, connector *testConnector) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)

	expected := testTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.directTxHash, Index: 0},
		Sequence:         expected,
	})
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: connector.txHash, Index: 0},
		Sequence:         0,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	return serializeTx(t, tx)
}

func makeClientCoopExitDirectFromCpfpTx(t *testing.T, leaf *testLeaf, refundDest keys.Public, connector *testConnector) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)

	expected := testTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		Sequence:         expected,
	})
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: connector.txHash, Index: 0},
		Sequence:         0,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	return serializeTx(t, tx)
}

func makeConnectorTx(t *testing.T) *testConnector {
	t.Helper()

	connectorPkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	connectorTx := wire.NewMsgTx(3)
	connectorTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0}, nil, nil))
	connectorTx.AddTxOut(wire.NewTxOut(200_000, connectorPkScript))

	return &testConnector{
		raw:    serializeTx(t, connectorTx),
		txHash: connectorTx.TxHash(),
	}
}

func handlerWithConfig() *BaseTransferHandler {
	return &BaseTransferHandler{config: &so.Config{}}
}

func withKnob(ctx context.Context, enabled bool) context.Context {
	v := 0.0
	if enabled {
		v = 1.0
	}
	k := knobs.NewFixedKnobs(map[string]float64{
		// Tests run in regtest
		knobs.KnobEnableStrictFinalizeSignature + "@REGTEST": v,
	})
	return knobs.InjectKnobsService(ctx, k)
}

func validateAndConstructBitcoinTransactionsForTest(
	t *testing.T,
	ctx context.Context,
	h *BaseTransferHandler,
	req *pb.StartTransferRequest,
	transferType st.TransferType,
	connectorTx []byte,
) error {
	t.Helper()

	cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap := loadLeafRefundMaps(req)

	refundDestPubkey, err := keys.ParsePublicKey(req.ReceiverIdentityPublicKey)
	require.NoError(t, err)

	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	leafUUIDs := make([]uuid.UUID, 0, len(cpfpLeafRefundMap))
	for leafID := range cpfpLeafRefundMap {
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return err
		}
		leafUUIDs = append(leafUUIDs, leafUUID)
	}

	leaves, err := db.TreeNode.Query().Where(treenode.IDIn(leafUUIDs...)).WithTree().All(ctx)
	if err != nil {
		return err
	}
	if len(leaves) != len(cpfpLeafRefundMap) {
		return fmt.Errorf("could not find all tree nodes: expected %d, found %d", len(cpfpLeafRefundMap), len(leaves))
	}

	return h.validateAndConstructBitcoinTransactions(ctx, req.GetTransferPackage(), transferType, leaves, cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap, refundDestPubkey, connectorTx)
}

// --- Tests ---
func TestValidateUserTxs_Legacy_Cpfp_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_Legacy_WithDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: makeClientDirectTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_Legacy_InvalidClientCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: []byte("not a tx")},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.ErrorContains(t, err, "CPFP refund tx validation failed")
}

func TestValidateUserTxs_Legacy_MissingDirectFromCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:             leaf.node.ID.String(),
				RefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				// Missing DirectFromCpfpRefundTxSigningJob
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.ErrorContains(t, err, "missing required direct from CPFP refund tx")
}

func TestValidateUserTxs_Legacy_WithoutDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create a zero node so direct refund tx remains optional.
	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
				// No DirectRefundTxSigningJob - should succeed since direct is optional
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_Legacy_InvalidDirectRefund_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: []byte("not a valid tx")},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.ErrorContains(t, err, "direct refund tx validation failed")
}

func TestValidateUserTxs_Package_WithDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	direct := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp},
			DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{direct},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp},
			KeyTweakPackage:            map[string][]byte{"noop": {}},
			UserSignature:              []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_Package_WithoutDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp},
			// No DirectLeavesToSend - should succeed since direct is optional
			KeyTweakPackage: map[string][]byte{"noop": {}},
			UserSignature:   []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_Package_InvalidDirectRefund_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	direct := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: []byte("not a valid tx")}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp},
			DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{direct},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp},
			KeyTweakPackage:            map[string][]byte{"noop": {}},
			UserSignature:              []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.ErrorContains(t, err, "direct refund tx validation failed")
}

func TestValidateUserTxs_Package_MismatchedCounts_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}
	orphan := &pb.UserSignedTxSigningJob{LeafId: uuid.New().String(), RawTx: directFromCpfp.RawTx}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp, orphan},
			KeyTweakPackage:            map[string][]byte{"noop": {}},
			UserSignature:              []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.ErrorContains(t, err, "mismatched number of leaves")
}

func TestValidateUserTxs_Package_UnknownLeafIDs_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	refundDest := keys.GeneratePrivateKey().Public()
	cpfp := &pb.UserSignedTxSigningJob{LeafId: uuid.New().String(), RawTx: []byte{0x00}} // invalid but we won't reach validation
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: cpfp.LeafId, RawTx: []byte{0x00}}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp},
			KeyTweakPackage:            map[string][]byte{"noop": {}},
			UserSignature:              []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.ErrorContains(t, err, "could not find all tree nodes")
}

func TestValidateUserTxs_Package_OrphanDirectLeaf_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}

	// Orphan direct leaf: ID not present in LeavesToSend
	orphanDirect := &pb.UserSignedTxSigningJob{LeafId: uuid.New().String(), RawTx: makeClientDirectTx(t, leaf, refundDest)}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp},
			DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{orphanDirect},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp},
			KeyTweakPackage:            map[string][]byte{"noop": {}},
			UserSignature:              []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
	require.ErrorContains(t, err, "found orphan leaf in DirectLeavesToSend")
}

func TestValidateUserTxs_Swap_Legacy_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:             leaf.node.ID.String(),
				RefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_Swap_Legacy_InvalidClientCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:             leaf.node.ID.String(),
				RefundTxSigningJob: &pb.SigningJob{RawTx: []byte("not a tx")},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap, nil)
	require.ErrorContains(t, err, "CPFP refund tx validation failed")
}

func TestValidateUserTxs_Swap_Package_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:    []*pb.UserSignedTxSigningJob{cpfp},
			KeyTweakPackage: map[string][]byte{"noop": {}},
			UserSignature:   []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_Swap_Package_UnknownLeafIDs_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	refundDest := keys.GeneratePrivateKey().Public()
	cpfp := &pb.UserSignedTxSigningJob{LeafId: uuid.New().String(), RawTx: []byte{0x00}}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:    []*pb.UserSignedTxSigningJob{cpfp},
			KeyTweakPackage: map[string][]byte{"noop": {}},
			UserSignature:   []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap, nil)
	require.ErrorContains(t, err, "could not find all tree nodes")
}

func TestValidateUserTxs_Swap_Package_IgnoresExtraDirectLeaves_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	direct := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp},
			DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{direct},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp},
			KeyTweakPackage:            map[string][]byte{"noop": {}},
			UserSignature:              []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap, nil)
	require.NoError(t, err)
}

func TestValidateUserTxs_CoopExit_Legacy_WithDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest, connector)},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest, connector)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit, connector.raw)
	require.NoError(t, err)
}

func TestValidateUserTxs_CoopExit_Legacy_WithoutDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create leaf that could have direct, but we don't provide it (direct is optional)
	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest, connector)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector)},
				// No DirectRefundTxSigningJob - should succeed since direct is optional
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit, connector.raw)
	require.NoError(t, err)
}

func TestValidateUserTxs_CoopExit_Legacy_InvalidDirectRefund_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest, connector)},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: []byte("not a valid tx")},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit, connector.raw)
	require.ErrorContains(t, err, "failed to parse refund transaction")
}

func TestValidateUserTxs_CoopExit_Legacy_InvalidClientCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: []byte("not a tx")},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit, connector.raw)
	require.ErrorContains(t, err, "failed to parse refund transaction")
}

func TestValidateUserTxs_CoopExit_Legacy_MissingDirectFromCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:             leaf.node.ID.String(),
				RefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest, connector)},
				// Missing DirectFromCpfpRefundTxSigningJob
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit, connector.raw)
	require.ErrorContains(t, err, "refund transaction is empty")
}

func TestValidateUserTxs_CoopExit_Legacy_MissingConnectorInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)

	// Use `makeClientCpfpfTx` so that cpfpTx has 1 input
	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit, connector.raw)
	require.ErrorContains(t, err, "expected 2 inputs in refund tx, got 1")
}

func TestValidateUserTxs_CoopExit_Legacy_ExceedInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create leaf with direct tx timelock > 0, which requires direct refund tx
	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)
	cpfpTxRaw := makeClientCoopExitCpfpTx(t, leaf, refundDest, connector)
	cpfpTx, err := common.TxFromRawTxBytes(cpfpTxRaw)
	require.NoError(t, err)

	cpfpTx.TxIn = append(cpfpTx.TxIn, cpfpTx.TxIn[1]) // Add another input to exceed expected count
	cpfpTxRawModified := serializeTx(t, cpfpTx)

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: cpfpTxRawModified},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector)},
			},
		},
	}

	h := handlerWithConfig()
	err = validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit, connector.raw)
	require.ErrorContains(t, err, "expected 2 inputs in refund tx, got 3")
}
