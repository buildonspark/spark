package handler

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func mustSerializeTx(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()
	bytes, err := common.SerializeTx(tx)
	if err != nil {
		t.Fatalf("failed to serialize tx: %v", err)
	}
	return bytes
}

func TestCreateTransfer_UsesNodeTxOutpoint_SucceedsWithCorruptedOldRefund(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	if err != nil {
		t.Fatalf("failed to get db client: %v", err)
	}

	senderPriv := keys.GeneratePrivateKey()
	senderPub := senderPriv.Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPub).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	p2tr, err := common.P2TRScriptFromPubKey(receiverPub)
	if err != nil {
		t.Fatalf("failed to build p2tr: %v", err)
	}

	nodeTx := &wire.MsgTx{Version: 2}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)
	nodeHash := nodeTx.TxHash()

	wrongParent := &wire.MsgTx{Version: 2}
	wrongParent.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	wrongParent.AddTxOut(common.EphemeralAnchorOutput())
	wrongHash := wrongParent.TxHash()

	const oldTimeLock uint32 = 600
	oldRefund := &wire.MsgTx{Version: 2}
	oldRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: wrongHash, Index: 0},
		Sequence:         oldTimeLock,
	})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 2}
	newRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock - spark.TimeLockInterval,
	})
	newRefund.AddTxOut(&wire.TxOut{Value: 0, PkScript: p2tr})
	newRefundBytes := mustSerializeTx(t, newRefund)

	// Create required signing keyshare edge
	secret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"key": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create signing keyshare: %v", err)
	}

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(senderPub).
		SetOwnerSigningPubkey(senderPub).
		SetSigningKeyshare(keyshare).
		SetRawTx(nodeBytes).
		SetRawRefundTx(oldRefundBytes).
		SetVout(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create leaf: %v", err)
	}

	leafCpfpRefundMap := map[string][]byte{
		leaf.ID.String(): newRefundBytes,
	}

	h := NewBaseTransferHandler(config)
	transferID := uuid.New()
	expiry := time.Now().Add(10 * time.Minute)

	_, _, err = h.createTransfer(
		ctx,
		nil,
		transferID,
		st.TransferTypeTransfer,
		expiry,
		senderPub,
		receiverPub,
		leafCpfpRefundMap,
		map[string][]byte{},
		map[string][]byte{},
		nil,
		TransferRoleCoordinator,
		false,
		"",
		uuid.Nil,
	)
	if err != nil {
		t.Fatalf("expected success when using nodeTx as expected outpoint, got error: %v", err)
	}
}

func TestCreateTransfer_FailsWithWrongPrevOutpoint(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	if err != nil {
		t.Fatalf("failed to get db client: %v", err)
	}

	senderPriv := keys.GeneratePrivateKey()
	senderPub := senderPriv.Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPub).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	p2tr, err := common.P2TRScriptFromPubKey(receiverPub)
	if err != nil {
		t.Fatalf("failed to build p2tr: %v", err)
	}
	nodeTx := &wire.MsgTx{Version: 2}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)

	const oldTimeLock uint32 = 500
	oldRefund := &wire.MsgTx{Version: 2}
	oldRefund.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: oldTimeLock})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 2}
	newRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 1},
		Sequence:         oldTimeLock - spark.TimeLockInterval,
	})
	newRefund.AddTxOut(&wire.TxOut{Value: 0, PkScript: p2tr})
	newRefundBytes := mustSerializeTx(t, newRefund)

	secret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"key": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create signing keyshare: %v", err)
	}

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(senderPub).
		SetOwnerSigningPubkey(senderPub).
		SetSigningKeyshare(keyshare).
		SetRawTx(nodeBytes).
		SetRawRefundTx(oldRefundBytes).
		SetVout(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create leaf: %v", err)
	}

	leafCpfpRefundMap := map[string][]byte{
		leaf.ID.String(): newRefundBytes,
	}

	h := NewBaseTransferHandler(config)
	_, _, err = h.createTransfer(
		ctx,
		nil,
		uuid.New(),
		st.TransferTypeTransfer,
		time.Now().Add(10*time.Minute),
		senderPub,
		receiverPub,
		leafCpfpRefundMap,
		map[string][]byte{},
		map[string][]byte{},
		nil,
		TransferRoleCoordinator,
		false,
		"",
		uuid.Nil,
	)
	if err == nil {
		t.Fatalf("expected error for wrong outpoint, got nil")
	}
}

// Test that the counter swap v3 transfer fails if the total value of the leaves does not match the total value of the primary transfer.
// When we implement fees, we will need to change it to validate a statement from the user that they accepted a certain amount.
func TestCreateTransfer_CounterSwapV3_FailsWithMismatchedAmount(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderPriv := keys.GeneratePrivateKey()
	senderPub := senderPriv.Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	// Create primary swap transfer with TotalValue = 2000
	primaryTransfer, err := client.Transfer.Create().
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiverPub).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetTotalValue(2000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetType(st.TransferTypePrimarySwapV3).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPub).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	p2tr, err := common.P2TRScriptFromPubKey(receiverPub)
	require.NoError(t, err)

	nodeTx := &wire.MsgTx{Version: 2}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)
	nodeHash := nodeTx.TxHash()

	const oldTimeLock uint32 = 600
	oldRefund := &wire.MsgTx{Version: 2}
	oldRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock,
	})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 2}
	newRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock - spark.TimeLockInterval,
	})
	newRefund.AddTxOut(&wire.TxOut{Value: 0, PkScript: p2tr})
	newRefundBytes := mustSerializeTx(t, newRefund)

	secret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"key": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	// Create leaf with Value = 1000 (different from primary transfer's 2000)
	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(senderPub).
		SetOwnerSigningPubkey(senderPub).
		SetSigningKeyshare(keyshare).
		SetRawTx(nodeBytes).
		SetRawRefundTx(oldRefundBytes).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	leafCpfpRefundMap := map[string][]byte{
		leaf.ID.String(): newRefundBytes,
	}

	h := NewBaseTransferHandler(config)
	transferID := uuid.New()
	expiry := time.Now().Add(10 * time.Minute)

	_, _, err = h.createTransfer(
		ctx,
		nil,
		transferID,
		st.TransferTypeCounterSwapV3,
		expiry,
		senderPub,
		receiverPub,
		leafCpfpRefundMap,
		map[string][]byte{},
		map[string][]byte{},
		nil,
		TransferRoleCoordinator,
		false,
		"",
		primaryTransfer.ID,
	)
	require.Error(t, err)
	expectedErrSubstring := "does not match counter transfer amount"
	require.ErrorContains(t, err, expectedErrSubstring)
}
