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
	connectorTx.AddTxOut(wire.NewTxOut(1_000, connectorPkScript))

	return &testConnector{
		raw:    serializeTx(t, connectorTx),
		txHash: connectorTx.TxHash(),
	}
}

func replaceFirstInputOutPoint(t *testing.T, rawTx []byte, outPoint wire.OutPoint) []byte {
	t.Helper()

	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	require.NotEmpty(t, tx.TxIn)
	tx.TxIn[0].PreviousOutPoint = outPoint
	return serializeTx(t, tx)
}

func handlerWithConfig() *BaseTransferHandler {
	return &BaseTransferHandler{config: &so.Config{}}
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

	refundDestPubkey, err := keys.ParsePublicKey(req.GetReceiverIdentityPublicKey())
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

func TestValidateUserTxs_Legacy_WrongRefundPrevout_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	wrongOutPoint := wire.OutPoint{Hash: chainhash.Hash{0x99}, Index: 0}

	tests := []struct {
		name        string
		req         *pb.StartTransferRequest
		errContains string
	}{
		{
			name: "cpfp",
			req: &pb.StartTransferRequest{
				ReceiverIdentityPublicKey: refundDest.Serialize(),
				LeavesToSend: []*pb.LeafRefundTxSigningJob{
					{
						LeafId: leaf.node.ID.String(),
						RefundTxSigningJob: &pb.SigningJob{
							RawTx: replaceFirstInputOutPoint(t, makeClientCpfpTx(t, leaf, refundDest), wrongOutPoint),
						},
						DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: makeClientDirectTx(t, leaf, refundDest)},
						DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
					},
				},
			},
			errContains: "CPFP refund tx validation failed",
		},
		{
			name: "direct",
			req: &pb.StartTransferRequest{
				ReceiverIdentityPublicKey: refundDest.Serialize(),
				LeavesToSend: []*pb.LeafRefundTxSigningJob{
					{
						LeafId:                   leaf.node.ID.String(),
						RefundTxSigningJob:       &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
						DirectRefundTxSigningJob: &pb.SigningJob{RawTx: replaceFirstInputOutPoint(t, makeClientDirectTx(t, leaf, refundDest), wrongOutPoint)},
						DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
							RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest),
						},
					},
				},
			},
			errContains: "direct refund tx validation failed",
		},
		{
			name: "direct-from-cpfp",
			req: &pb.StartTransferRequest{
				ReceiverIdentityPublicKey: refundDest.Serialize(),
				LeavesToSend: []*pb.LeafRefundTxSigningJob{
					{
						LeafId:                   leaf.node.ID.String(),
						RefundTxSigningJob:       &pb.SigningJob{RawTx: makeClientCpfpTx(t, leaf, refundDest)},
						DirectRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientDirectTx(t, leaf, refundDest)},
						DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
							RawTx: replaceFirstInputOutPoint(t, makeClientDirectFromCpfpTx(t, leaf, refundDest), wrongOutPoint),
						},
					},
				},
			},
			errContains: "direct from CPFP refund tx validation failed",
		},
	}

	h := handlerWithConfig()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, tt.req, st.TransferTypeTransfer, nil)
			require.ErrorContains(t, err, tt.errContains)
			require.ErrorContains(t, err, "expected previous outpoint")
		})
	}
}

func TestValidateUserTxs_Package_WithDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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

func TestValidateUserTxs_Package_WrongRefundPrevout_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	wrongOutPoint := wire.OutPoint{Hash: chainhash.Hash{0x99}, Index: 0}

	tests := []struct {
		name        string
		pkg         *pb.TransferPackage
		errContains string
	}{
		{
			name: "cpfp",
			pkg: &pb.TransferPackage{
				LeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: replaceFirstInputOutPoint(t, makeClientCpfpTx(t, leaf, refundDest), wrongOutPoint)},
				},
				DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectTx(t, leaf, refundDest)},
				},
				DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
				},
				KeyTweakPackage: map[string][]byte{"noop": {}},
				UserSignature:   []byte{1},
			},
			errContains: "CPFP refund tx validation failed",
		},
		{
			name: "direct",
			pkg: &pb.TransferPackage{
				LeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				},
				DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: replaceFirstInputOutPoint(t, makeClientDirectTx(t, leaf, refundDest), wrongOutPoint)},
				},
				DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)},
				},
				KeyTweakPackage: map[string][]byte{"noop": {}},
				UserSignature:   []byte{1},
			},
			errContains: "direct refund tx validation failed",
		},
		{
			name: "direct-from-cpfp",
			pkg: &pb.TransferPackage{
				LeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)},
				},
				DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectTx(t, leaf, refundDest)},
				},
				DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.node.ID.String(), RawTx: replaceFirstInputOutPoint(t, makeClientDirectFromCpfpTx(t, leaf, refundDest), wrongOutPoint)},
				},
				KeyTweakPackage: map[string][]byte{"noop": {}},
				UserSignature:   []byte{1},
			},
			errContains: "direct from CPFP refund tx validation failed",
		},
	}

	h := handlerWithConfig()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &pb.StartTransferRequest{
				ReceiverIdentityPublicKey: refundDest.Serialize(),
				TransferPackage:           tt.pkg,
			}
			err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, st.TransferTypeTransfer, nil)
			require.ErrorContains(t, err, tt.errContains)
			require.ErrorContains(t, err, "expected previous outpoint")
		})
	}
}

func TestValidateUserTxs_Package_MismatchedCounts_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectFromCpfpTx(t, leaf, refundDest)}
	orphan := &pb.UserSignedTxSigningJob{LeafId: uuid.New().String(), RawTx: directFromCpfp.GetRawTx()}

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

	refundDest := keys.GeneratePrivateKey().Public()
	cpfp := &pb.UserSignedTxSigningJob{LeafId: uuid.New().String(), RawTx: []byte{0x00}} // invalid but we won't reach validation
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: cpfp.GetLeafId(), RawTx: []byte{0x00}}

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

func TestValidateUserTxs_Swap_Package_ValidatesProvidedDirectLeaves_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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

func TestValidateUserTxs_Swap_Package_InvalidDirectRefund_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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
	for _, transferType := range []st.TransferType{st.TransferTypeSwap, st.TransferTypeCounterSwap, st.TransferTypePrimarySwapV3, st.TransferTypeCounterSwapV3} {
		t.Run(string(transferType), func(t *testing.T) {
			err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, transferType, nil)
			require.ErrorContains(t, err, "direct refund tx validation failed")
		})
	}
}

func TestValidateUserTxs_Swap_Package_InvalidDirectFromCpfpRefund_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()

	cpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientCpfpTx(t, leaf, refundDest)}
	direct := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: makeClientDirectTx(t, leaf, refundDest)}
	directFromCpfp := &pb.UserSignedTxSigningJob{LeafId: leaf.node.ID.String(), RawTx: []byte("not a valid tx")}

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
	for _, transferType := range []st.TransferType{st.TransferTypeSwap, st.TransferTypeCounterSwap, st.TransferTypePrimarySwapV3, st.TransferTypeCounterSwapV3} {
		t.Run(string(transferType), func(t *testing.T) {
			err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, req, transferType, nil)
			require.ErrorContains(t, err, "direct from CPFP refund tx validation failed")
		})
	}
}

func TestValidateUserTxs_CoopExit_Legacy_WithDirect_Success(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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
	require.ErrorContains(t, err, "failed to parse direct refund transaction")
}

func TestValidateUserTxs_CoopExit_Legacy_InvalidClientCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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
	require.ErrorContains(t, err, "failed to parse cpfp refund transaction")
}

func TestValidateUserTxs_CoopExit_Legacy_MissingDirectFromCpfp_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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
	require.ErrorContains(t, err, "direct-from-CPFP refund tx is required")
}

func TestValidateUserTxs_CoopExit_Legacy_MissingConnectorInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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
	require.ErrorContains(t, err, "cpfp refund tx must have exactly 2 inputs when connector tx is provided, got 1")
}

func TestValidateUserTxs_CoopExit_Legacy_ExceedInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

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
	require.ErrorContains(t, err, "cpfp refund tx must have exactly 2 inputs when connector tx is provided, got 3")
}

func TestValidateUserTxs_CoopExit_Legacy_WrongNodeInput_Error(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	leaf := createDbLeaf(t, ctx, true)
	refundDest := keys.GeneratePrivateKey().Public()
	connector := makeConnectorTx(t)
	wrongOutPoint := wire.OutPoint{Hash: chainhash.Hash{0x99}, Index: 0}

	tests := []struct {
		name        string
		req         *pb.StartTransferRequest
		errContains string
	}{
		{
			name: "cpfp",
			req: &pb.StartTransferRequest{
				ReceiverIdentityPublicKey: refundDest.Serialize(),
				LeavesToSend: []*pb.LeafRefundTxSigningJob{
					{
						LeafId: leaf.node.ID.String(),
						RefundTxSigningJob: &pb.SigningJob{
							RawTx: replaceFirstInputOutPoint(t, makeClientCoopExitCpfpTx(t, leaf, refundDest, connector), wrongOutPoint),
						},
						DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest, connector)},
						DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector)},
					},
				},
			},
			errContains: "CPFP refund validation failed",
		},
		{
			name: "direct",
			req: &pb.StartTransferRequest{
				ReceiverIdentityPublicKey: refundDest.Serialize(),
				LeavesToSend: []*pb.LeafRefundTxSigningJob{
					{
						LeafId:                   leaf.node.ID.String(),
						RefundTxSigningJob:       &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest, connector)},
						DirectRefundTxSigningJob: &pb.SigningJob{RawTx: replaceFirstInputOutPoint(t, makeClientCoopExitDirectTx(t, leaf, refundDest, connector), wrongOutPoint)},
						DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
							RawTx: makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector),
						},
					},
				},
			},
			errContains: "direct refund validation failed",
		},
		{
			name: "direct-from-cpfp",
			req: &pb.StartTransferRequest{
				ReceiverIdentityPublicKey: refundDest.Serialize(),
				LeavesToSend: []*pb.LeafRefundTxSigningJob{
					{
						LeafId:                   leaf.node.ID.String(),
						RefundTxSigningJob:       &pb.SigningJob{RawTx: makeClientCoopExitCpfpTx(t, leaf, refundDest, connector)},
						DirectRefundTxSigningJob: &pb.SigningJob{RawTx: makeClientCoopExitDirectTx(t, leaf, refundDest, connector)},
						DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
							RawTx: replaceFirstInputOutPoint(t, makeClientCoopExitDirectFromCpfpTx(t, leaf, refundDest, connector), wrongOutPoint),
						},
					},
				},
			},
			errContains: "direct-from-CPFP refund validation failed",
		},
	}

	h := handlerWithConfig()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAndConstructBitcoinTransactionsForTest(t, ctx, h, tt.req, st.TransferTypeCooperativeExit, connector.raw)
			require.ErrorContains(t, err, tt.errContains)
			require.ErrorContains(t, err, "refund tx input 0 does not reference the node tx")
		})
	}
}
