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

func makeClientCoopExitCpfpTx(t *testing.T, leaf *testLeaf, refundDest keys.Public) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	randomPrivateKey := keys.GeneratePrivateKey()
	randomTxHash, err := chainhash.NewHash(randomPrivateKey.Serialize())
	require.NoError(t, err)

	expectedCpfp := uint32(testTimeLock - spark.TimeLockInterval)
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		Sequence:         expectedCpfp,
	})
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: *randomTxHash, Index: 0},
		Sequence:         0,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	return serializeTx(t, tx)
}

func makeClientCoopExitDirectTx(t *testing.T, leaf *testLeaf, refundDest keys.Public) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	randomPrivateKey := keys.GeneratePrivateKey()
	randomTxHash, err := chainhash.NewHash(randomPrivateKey.Serialize())
	require.NoError(t, err)

	expected := testTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.directTxHash, Index: 0},
		Sequence:         expected,
	})
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: *randomTxHash, Index: 0},
		Sequence:         0,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	return serializeTx(t, tx)
}

func makeClientCoopExitDirectFromCpfpTx(t *testing.T, leaf *testLeaf, refundDest keys.Public) []byte {
	userScript, err := common.P2TRScriptFromPubKey(refundDest)
	require.NoError(t, err)
	randomPrivateKey := keys.GeneratePrivateKey()
	randomTxHash, err := chainhash.NewHash(randomPrivateKey.Serialize())
	require.NoError(t, err)

	expected := testTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		Sequence:         expected,
	})
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: *randomTxHash, Index: 0},
		Sequence:         0,
	})
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	return serializeTx(t, tx)
}

func handlerWithConfig() *BaseTransferHandler {
	return &BaseTransferHandler{config: &so.Config{}}
}

func withKnob(ctx context.Context, enabled bool) context.Context {
	v := 0.0
	if enabled {
		v = 1.0
	}
	k := knobs.NewFixedKnobs(map[string]float64{knobs.KnobSoEnhancedBitcoinTxValidation: v})
	return knobs.InjectKnobsService(ctx, k)
}

func loadLeafRefundMaps(req *pb.StartTransferRequest) (map[string][]byte, map[string][]byte, map[string][]byte) {
	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)

	if req.TransferPackage != nil {
		for _, leaf := range req.TransferPackage.LeavesToSend {
			cpfpLeafRefundMap[leaf.LeafId] = leaf.RawTx
		}
		for _, leaf := range req.TransferPackage.DirectLeavesToSend {
			directLeafRefundMap[leaf.LeafId] = leaf.RawTx
		}
		for _, leaf := range req.TransferPackage.DirectFromCpfpLeavesToSend {
			directFromCpfpLeafRefundMap[leaf.LeafId] = leaf.RawTx
		}
	} else {
		for _, leaf := range req.LeavesToSend {
			cpfpLeafRefundMap[leaf.LeafId] = leaf.GetRefundTxSigningJob().GetRawTx()
			directLeafRefundMap[leaf.LeafId] = leaf.GetDirectRefundTxSigningJob().GetRawTx()
			directFromCpfpLeafRefundMap[leaf.LeafId] = leaf.GetDirectFromCpfpRefundTxSigningJob().GetRawTx()
		}
	}
	return cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap
}

func validateAndConstructBitcoinTransactionsForTest(t *testing.T, ctx context.Context, h *BaseTransferHandler, req *pb.StartTransferRequest, transferType st.TransferType) error {
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

	return h.validateAndConstructBitcoinTransactions(ctx, req, transferType, leaves, cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap, refundDestPubkey)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
	require.ErrorContains(t, err, "missing required direct from CPFP refund tx")
}

func TestValidateUserTxs_Legacy_MissingDirectWhenRequired_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create leaf with direct tx timelock > 0, which requires direct refund tx
	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
				// Missing DirectRefundTxSigningJob - should fail because node has direct tx with timelock
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
	require.ErrorContains(t, err, "missing required direct refund tx")
}

func TestValidateUserTxs_Legacy_UnexpectedDirectWhenNotRequired_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create leaf with direct tx timelock = 0, which does NOT require direct refund tx
	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: makeClientDirectTx(t, leaf, refundDest)},
				// DirectRefundTxSigningJob is present but should NOT be - should fail
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
	require.ErrorContains(t, err, "unexpected direct refund tx")
}

func TestValidateUserTxs_Package_Success(t *testing.T) {
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
	require.NoError(t, err)
}

func TestValidateUserTxs_Package_MissingDirectLeaf_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}

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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
	require.ErrorContains(t, err, "missing required direct refund tx")
}

func TestValidateUserTxs_Package_UnexpectedDirectLeaf_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create a leaf whose DirectTx has timelock = 0 so no direct refund is required
	leaf := createDbLeaf(t, ctx, false)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
	require.ErrorContains(t, err, "unexpected direct refund tx")
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap)
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeSwap)
	require.NoError(t, err)
}

func TestValidateUserTxs_CoopExit_Legacy_WithDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.NoError(t, err)
}

func TestValidateUserTxs_CoopExit_Legacy_InvalidClientCpfp_Error(t *testing.T) {
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
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "failed to parse raw transaction")
}

func TestValidateUserTxs_CoopExit_Legacy_MissingDirectFromCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:             leaf.node.ID.String(),
				RefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)},
				// Missing DirectFromCpfpRefundTxSigningJob
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "raw transaction is empty")
}

func TestValidateUserTxs_CoopExit_Legacy_MissingDirectWhenRequired_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create leaf with direct tx timelock > 0, which requires direct refund tx
	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)},
				// Missing DirectRefundTxSigningJob - should fail because node has direct tx with timelock
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "missing required direct refund tx")
}

func TestValidateUserTxs_CoopExit_Legacy_UnexpectedDirectWhenNotRequired_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create leaf with direct tx timelock = 0, which does NOT require direct refund tx
	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest)},
				// DirectRefundTxSigningJob is present but should NOT be - should fail
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "unexpected direct refund tx")
}

func TestValidateUserTxs_CoopExit_Legacy_MissingConnectorInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	// Use `makeClientCpfpfTx` so that cpfpTx has 1 input
	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           leaf.node.ID.String(),
				RefundTxSigningJob:               &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "out of bounds vin")
}

func TestValidateUserTxs_CoopExit_Legacy_ExceedInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create leaf with direct tx timelock > 0, which requires direct refund tx
	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	cpfpTxRaw := makeClientCoopExitCpfpTx(t, leaf, refundDest)
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
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)},
			},
		},
	}

	h := handlerWithConfig()
	err = validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "transaction does not match expected construction")
}

func TestValidateUserTxs_CoopExit_Package_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)}
	direct := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}

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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.NoError(t, err)
}

func TestValidateUserTxs_CoopExit_Package_MissingDirectLeaf_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}

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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "missing required direct refund tx")
}

func TestValidateUserTxs_CoopExit_Package_UnexpectedDirectLeaf_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	// Create a leaf whose DirectTx has timelock = 0 so no direct refund is required
	leaf := createDbLeaf(t, ctx, false)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)}
	direct := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}

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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "unexpected direct refund tx")
}

func TestValidateUserTxs_CoopExit_Package_MismatchedCounts_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "mismatched number of leaves")
}

func TestValidateUserTxs_CoopExit_Package_UnknownLeafIDs_Error(t *testing.T) {
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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "could not find all tree nodes")
}

func TestValidateUserTxs_CoopExit_Package_DuplicateLeafIDs_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	// Create two entries in LeavesToSend with the same canonical UUID string
	cpfp1 := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)}
	cpfp2 := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)}
	// DirectFromCpfp must match count of LeavesToSend
	directFromCpfp1 := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}
	directFromCpfp2 := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}

	req := &pb.StartTransferRequest{
		ReceiverIdentityPublicKey: refundDest.Serialize(),
		TransferPackage: &pb.TransferPackage{
			LeavesToSend:               []*pb.UserSignedTxSigningJob{cpfp1, cpfp2},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{directFromCpfp1, directFromCpfp2},
			KeyTweakPackage:            map[string][]byte{"noop": {}},
			UserSignature:              []byte{1},
		},
	}

	h := handlerWithConfig()
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	// validateLeaves should detect duplicate leaf IDs
	require.ErrorContains(t, err, "duplicate leaf id")
}

func TestValidateUserTxs_CoopExit_Package_OrphanDirectLeaf_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}

	// Orphan direct leaf: ID not present in LeavesToSend
	orphanDirect := &pb.UserSignedTxSigningJob{LeafId: uuid.New().String(), RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest)}

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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "found orphan leaf in DirectLeavesToSend")
}

func TestValidateUserTxs_CoopExit_Package_MissingConnectorInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	// Create CPFP tx with 1 input
	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}

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
	err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "out of bounds vin")
}

func TestValidateUserTxs_CoopExit_Package_ExceedInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfpTxRaw := makeClientCoopExitCpfpTx(t, leaf, refundDest)
	cpfpTx, err := common.TxFromRawTxBytes(cpfpTxRaw)
	require.NoError(t, err)

	cpfpTx.TxIn = append(cpfpTx.TxIn, cpfpTx.TxIn[1]) // Add another input to exceed expected count
	cpfpTxRawModified := serializeTx(t, cpfpTx)
	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: cpfpTxRawModified}

	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest)}

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
	err = validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeCooperativeExit)
	require.ErrorContains(t, err, "transaction does not match expected construction")
}
