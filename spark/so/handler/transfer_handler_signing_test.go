package handler

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignRefundsWithPregeneratedNonce_NilPackage(t *testing.T) {
	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		nil, // nil package
		nil, // leafMap
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transfer package is nil")
}

func TestSignRefundsWithPregeneratedNonce_LeafNotInMap(t *testing.T) {
	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "missing-leaf", RawTx: []byte{0x01}},
		},
	}
	leafMap := make(map[string]*ent.TreeNode)

	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		pkg,
		leafMap,
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaf missing-leaf not found in leafMap")
}

func TestSignRefundsWithPregeneratedNonce_DirectLeafNotInMap(t *testing.T) {
	pkg := &pb.TransferPackage{
		DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "missing-direct-leaf", RawTx: []byte{0x01}},
		},
	}
	leafMap := make(map[string]*ent.TreeNode)

	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		pkg,
		leafMap,
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaf missing-direct-leaf not found in leafMap")
}

func TestSignRefundsWithPregeneratedNonce_DirectFromCpfpLeafNotInMap(t *testing.T) {
	pkg := &pb.TransferPackage{
		DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "missing-dcl", RawTx: []byte{0x01}},
		},
	}
	leafMap := make(map[string]*ent.TreeNode)

	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		pkg,
		leafMap,
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaf missing-dcl not found in leafMap")
}

func TestSignRefundsRejectsMultiInputRefundWithoutConnector(t *testing.T) {
	leafID := uuid.New().String()
	leaf := testRefundSigningLeaf(t, leafID)
	refundTx := testRefundTx(t, testLeafOutpoint(t, leaf.RawTx), wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0})

	_, err := signRefunds(
		t.Context(),
		nil,
		&pb.StartTransferRequest{
			LeavesToSend: []*pb.LeafRefundTxSigningJob{{
				LeafId: leafID,
				RefundTxSigningJob: &pb.SigningJob{
					RawTx: serializeTx(t, refundTx),
				},
			}},
		},
		map[string]*ent.TreeNode{leafID: leaf},
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cpfp refund tx has 2 inputs but no connector tx was provided")
}

func TestSignRefundsRejectsTooManyInputsWithoutConnector(t *testing.T) {
	leafID := uuid.New().String()
	leaf := testRefundSigningLeaf(t, leafID)
	refundTx := testRefundTx(
		t,
		testLeafOutpoint(t, leaf.RawTx),
		wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0},
		wire.OutPoint{Hash: chainhash.Hash{3}, Index: 0},
	)

	_, err := signRefunds(
		t.Context(),
		nil,
		&pb.StartTransferRequest{
			LeavesToSend: []*pb.LeafRefundTxSigningJob{{
				LeafId: leafID,
				RefundTxSigningJob: &pb.SigningJob{
					RawTx: serializeTx(t, refundTx),
				},
			}},
		},
		map[string]*ent.TreeNode{leafID: leaf},
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cpfp refund tx has 3 inputs; refund transactions support at most 2 inputs")
}

func TestSignRefundsRejectsTooManyConnectorInputs(t *testing.T) {
	leafID := uuid.New().String()
	leaf := testRefundSigningLeaf(t, leafID)
	connectorTx, connectorOutpoint := testConnectorTx(t)
	refundTx := testRefundTx(
		t,
		testLeafOutpoint(t, leaf.RawTx),
		connectorOutpoint,
		wire.OutPoint{Hash: chainhash.Hash{3}, Index: 0},
	)

	_, err := signRefunds(
		t.Context(),
		nil,
		&pb.StartTransferRequest{
			LeavesToSend: []*pb.LeafRefundTxSigningJob{{
				LeafId: leafID,
				RefundTxSigningJob: &pb.SigningJob{
					RawTx: serializeTx(t, refundTx),
				},
			}},
		},
		map[string]*ent.TreeNode{leafID: leaf},
		keys.Public{}, keys.Public{}, keys.Public{},
		serializeTx(t, connectorTx),
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cpfp refund tx must have exactly 2 inputs when connector tx is provided, got 3")
}

func TestValidateRefundInputCountForConnectorRejectsSingleInputWithConnector(t *testing.T) {
	connectorTx, _ := testConnectorTx(t)
	connectorPrevOuts, err := parseConnectorTxOutputs(serializeTx(t, connectorTx))
	require.NoError(t, err)

	refundTx := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0})
	err = validateRefundInputCountForConnector(refundTx, connectorPrevOuts, "cpfp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cpfp refund tx must have exactly 2 inputs when connector tx is provided, got 1")
}

func TestValidateCoopExitConnectorLayout(t *testing.T) {
	leafA := uuid.New().String()
	leafB := uuid.New().String()

	t.Run("accepts distinct leaf connector outputs and fee bump output", func(t *testing.T) {
		connectorTx := testConnectorTxWithOutputCount(t, 2)
		connectorPrevOuts, err := parseConnectorTxOutputs(serializeTx(t, connectorTx))
		require.NoError(t, err)

		leafARefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{10}, Index: 0}, testConnectorOutpoint(connectorTx, 0))
		err = validateCoopExitConnectorLayout(
			connectorPrevOuts,
			map[string][]byte{leafA: serializeTx(t, leafARefund)},
			nil,
			map[string][]byte{leafA: serializeTx(t, leafARefund)},
		)
		require.NoError(t, err)
	})

	t.Run("rejects connector output count mismatch", func(t *testing.T) {
		connectorTx := testConnectorTxWithOutputCount(t, 1)
		connectorPrevOuts, err := parseConnectorTxOutputs(serializeTx(t, connectorTx))
		require.NoError(t, err)

		leafARefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{11}, Index: 0}, testConnectorOutpoint(connectorTx, 0))
		err = validateCoopExitConnectorLayout(
			connectorPrevOuts,
			map[string][]byte{leafA: serializeTx(t, leafARefund)},
			nil,
			map[string][]byte{leafA: serializeTx(t, leafARefund)},
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "one output per leaf plus one fee-bump output")
	})

	t.Run("rejects reused connector output across leaves", func(t *testing.T) {
		connectorTx := testConnectorTxWithOutputCount(t, 3)
		connectorPrevOuts, err := parseConnectorTxOutputs(serializeTx(t, connectorTx))
		require.NoError(t, err)

		leafARefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{12}, Index: 0}, testConnectorOutpoint(connectorTx, 0))
		leafBRefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{13}, Index: 0}, testConnectorOutpoint(connectorTx, 0))
		err = validateCoopExitConnectorLayout(
			connectorPrevOuts,
			map[string][]byte{
				leafA: serializeTx(t, leafARefund),
				leafB: serializeTx(t, leafBRefund),
			},
			nil,
			map[string][]byte{
				leafA: serializeTx(t, leafARefund),
				leafB: serializeTx(t, leafBRefund),
			},
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "used by multiple leaves")
	})

	t.Run("rejects fee-bump output as leaf connector output", func(t *testing.T) {
		connectorTx := testConnectorTxWithOutputCount(t, 2)
		connectorPrevOuts, err := parseConnectorTxOutputs(serializeTx(t, connectorTx))
		require.NoError(t, err)

		leafARefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{14}, Index: 0}, testConnectorOutpoint(connectorTx, 1))
		err = validateCoopExitConnectorLayout(
			connectorPrevOuts,
			map[string][]byte{leafA: serializeTx(t, leafARefund)},
			nil,
			map[string][]byte{leafA: serializeTx(t, leafARefund)},
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "only indexes 0 through 0 are leaf connector outputs")
	})

	t.Run("rejects mismatched connector outputs within one leaf", func(t *testing.T) {
		connectorTx := testConnectorTxWithOutputCount(t, 3)
		connectorPrevOuts, err := parseConnectorTxOutputs(serializeTx(t, connectorTx))
		require.NoError(t, err)

		cpfpRefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{15}, Index: 0}, testConnectorOutpoint(connectorTx, 0))
		directFromCpfpRefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{15}, Index: 0}, testConnectorOutpoint(connectorTx, 1))
		leafBRefund := testRefundTx(t, wire.OutPoint{Hash: chainhash.Hash{16}, Index: 0}, testConnectorOutpoint(connectorTx, 1))
		err = validateCoopExitConnectorLayout(
			connectorPrevOuts,
			map[string][]byte{
				leafA: serializeTx(t, cpfpRefund),
				leafB: serializeTx(t, leafBRefund),
			},
			nil,
			map[string][]byte{
				leafA: serializeTx(t, directFromCpfpRefund),
				leafB: serializeTx(t, leafBRefund),
			},
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "must spend the same connector output")
	})
}

func TestSignRefundsWithPregeneratedNonceRejectsMultiInputRefundWithoutConnector(t *testing.T) {
	testCases := []struct {
		name        string
		buildPkg    func(leafID string, leaf *ent.TreeNode) *pb.TransferPackage
		expectedErr string
	}{
		{
			name: "cpfp",
			buildPkg: func(leafID string, leaf *ent.TreeNode) *pb.TransferPackage {
				refundTx := testRefundTx(t, testLeafOutpoint(t, leaf.RawTx), wire.OutPoint{Hash: chainhash.Hash{2}, Index: 0})
				return &pb.TransferPackage{
					LeavesToSend: []*pb.UserSignedTxSigningJob{{
						LeafId: leafID,
						RawTx:  serializeTx(t, refundTx),
					}},
				}
			},
			expectedErr: "cpfp refund tx has 2 inputs but no connector tx was provided",
		},
		{
			name: "direct",
			buildPkg: func(leafID string, leaf *ent.TreeNode) *pb.TransferPackage {
				refundTx := testRefundTx(t, testLeafOutpoint(t, leaf.DirectTx), wire.OutPoint{Hash: chainhash.Hash{3}, Index: 0})
				return &pb.TransferPackage{
					DirectLeavesToSend: []*pb.UserSignedTxSigningJob{{
						LeafId: leafID,
						RawTx:  serializeTx(t, refundTx),
					}},
				}
			},
			expectedErr: "direct refund tx has 2 inputs but no connector tx was provided",
		},
		{
			name: "direct-from-cpfp",
			buildPkg: func(leafID string, leaf *ent.TreeNode) *pb.TransferPackage {
				refundTx := testRefundTx(t, testLeafOutpoint(t, leaf.RawTx), wire.OutPoint{Hash: chainhash.Hash{4}, Index: 0})
				return &pb.TransferPackage{
					DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{{
						LeafId: leafID,
						RawTx:  serializeTx(t, refundTx),
					}},
				}
			},
			expectedErr: "direct-from-cpfp refund tx has 2 inputs but no connector tx was provided",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			leafID := uuid.New().String()
			leaf := testRefundSigningLeaf(t, leafID)
			pkg := tc.buildPkg(leafID, leaf)

			_, _, _, err := SignRefundsWithPregeneratedNonce(
				t.Context(),
				nil,
				"test-transfer-id",
				pkg,
				map[string]*ent.TreeNode{leafID: leaf},
				keys.Public{}, keys.Public{}, keys.Public{},
				nil,
			)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
		})
	}
}

func TestApplySignaturesToCoopExitTransactionsAndVerifyRejectsMultiInputRefundWithoutConnector(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	leaf := createDbLeaf(t, ctx, false)
	leafID := leaf.node.ID.String()
	refundTx := testRefundTx(
		t,
		wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		wire.OutPoint{Hash: chainhash.Hash{5}, Index: 0},
	)

	_, err := applySignaturesToCoopExitTransactionsAndVerify(
		ctx,
		map[string][]byte{leafID: serializeTx(t, refundTx)},
		map[string][]byte{leafID: {1, 2, 3}},
		false,
		nil,
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "coop-exit refund tx has 2 inputs but no connector tx was provided")
}

func TestUpdateTransferLeavesSignaturesRejectsMultiInputRefundWithoutConnector(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	leaf := createDbLeaf(t, ctx, false)

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	transfer, err := dbClient.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusReceiverRefundSigned).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetReceiverIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetTotalValue(uint64(testSourceValue)).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	refundTx := testRefundTx(
		t,
		wire.OutPoint{Hash: leaf.nodeTxHash, Index: 0},
		wire.OutPoint{Hash: chainhash.Hash{6}, Index: 0},
	)
	_, err = dbClient.TransferLeaf.Create().
		SetLeaf(leaf.node).
		SetTransfer(transfer).
		SetPreviousRefundTx(leaf.node.RawRefundTx).
		SetIntermediateRefundTx(serializeTx(t, refundTx)).
		Save(ctx)
	require.NoError(t, err)

	handler := NewTransferHandler(&so.Config{})
	err = handler.UpdateTransferLeavesSignatures(
		ctx,
		transfer,
		map[string][]byte{leaf.node.ID.String(): make([]byte, 64)},
		map[string][]byte{},
		map[string][]byte{},
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid cpfp refund tx")
	require.Contains(t, err.Error(), "cpfp refund tx has 2 inputs but no connector tx was provided")
}

// --- rollbackTransferInit tests ---

func TestRollbackTransferInit_NoTxInContext(t *testing.T) {
	h := &TransferHandler{}
	err := h.rollbackTransferInit(t.Context(), uuid.New(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to get database transaction")
}

func testRefundSigningLeaf(t *testing.T, leafID string) *ent.TreeNode {
	t.Helper()

	pkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)
	nodeTx := newTestTx(testSourceValue, 0, nil, pkScript)
	directTx := newTestTx(testSourceValue, 0, nil, pkScript)

	parsedLeafID, err := uuid.Parse(leafID)
	require.NoError(t, err)
	return &ent.TreeNode{
		ID:       parsedLeafID,
		RawTx:    serializeTx(t, nodeTx),
		DirectTx: serializeTx(t, directTx),
	}
}

func testLeafOutpoint(t *testing.T, rawLeafTx []byte) wire.OutPoint {
	t.Helper()

	leafTx, err := common.TxFromRawTxBytes(rawLeafTx)
	require.NoError(t, err)
	return wire.OutPoint{Hash: leafTx.TxHash(), Index: 0}
}

func testRefundTx(t *testing.T, inputs ...wire.OutPoint) *wire.MsgTx {
	t.Helper()

	pkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	tx := wire.NewMsgTx(3)
	for _, input := range inputs {
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: input})
	}
	tx.AddTxOut(wire.NewTxOut(testSourceValue, pkScript))
	return tx
}

func testConnectorTx(t *testing.T) (*wire.MsgTx, wire.OutPoint) {
	t.Helper()

	tx := testConnectorTxWithOutputCount(t, 1)
	return tx, testConnectorOutpoint(tx, 0)
}

func testConnectorTxWithOutputCount(t *testing.T, outputCount int) *wire.MsgTx {
	t.Helper()

	pkScript, err := common.P2TRScriptFromPubKey(keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}})
	for range outputCount {
		tx.AddTxOut(wire.NewTxOut(testSourceValue, pkScript))
	}
	return tx
}

func testConnectorOutpoint(tx *wire.MsgTx, index uint32) wire.OutPoint {
	return wire.OutPoint{Hash: tx.TxHash(), Index: index}
}

func TestRollbackTransferInit_NoTxInContext_WithCancelGossip(t *testing.T) {
	h := &TransferHandler{}
	err := h.rollbackTransferInit(t.Context(), uuid.New(), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to get database transaction")
}
