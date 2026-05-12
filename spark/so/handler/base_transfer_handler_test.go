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
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
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

func TestValidateLeafRefundTxInputRejectsZeroTimelock(t *testing.T) {
	refundTx := wire.NewMsgTx(3)
	expectedOutPoint := wire.OutPoint{}
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: expectedOutPoint,
		Sequence:         0,
	})

	err := validateLeafRefundTxInput(refundTx, spark.TimeLockInterval, &expectedOutPoint, 1)
	require.ErrorContains(t, err, "time lock on the new refund tx must be greater than 0")
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

	nodeTx := &wire.MsgTx{Version: 3}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)
	nodeHash := nodeTx.TxHash()

	wrongParent := &wire.MsgTx{Version: 3}
	wrongParent.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	wrongParent.AddTxOut(common.EphemeralAnchorOutput())
	wrongHash := wrongParent.TxHash()

	const oldTimeLock uint32 = 600
	oldRefund := &wire.MsgTx{Version: 3}
	oldRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: wrongHash, Index: 0},
		Sequence:         oldTimeLock,
	})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 3}
	newRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock - spark.TimeLockInterval,
	})
	newRefund.AddTxOut(&wire.TxOut{Value: nodeTx.TxOut[0].Value, PkScript: p2tr})
	newRefund.AddTxOut(common.EphemeralAnchorOutput())
	newRefundBytes := mustSerializeTx(t, newRefund)

	newDirectFromCpfpRefund := &wire.MsgTx{Version: 3}
	newDirectFromCpfpRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset,
	})
	newDirectFromCpfpRefund.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(nodeTx.TxOut[0].Value), PkScript: p2tr})
	newDirectFromCpfpRefundBytes := mustSerializeTx(t, newDirectFromCpfpRefund)

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
	leafDirectFromCpfpRefundMap := map[string][]byte{
		leaf.ID.String(): newDirectFromCpfpRefundBytes,
	}

	h := NewBaseTransferHandler(config)
	transferID := uuid.New()
	expiry := time.Now().Add(10 * time.Minute)

	_, _, err = h.createTransfer(
		ctx,
		transferID,
		nil,
		st.TransferTypeTransfer,
		expiry,
		senderPub,
		receiverPub,
		leafCpfpRefundMap,
		map[string][]byte{},
		leafDirectFromCpfpRefundMap,
		nil,
		TransferRoleCoordinator,
		false,
		"",
		uuid.Nil,
		nil,
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
	nodeTx := &wire.MsgTx{Version: 3}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)

	const oldTimeLock uint32 = 500
	oldRefund := &wire.MsgTx{Version: 3}
	oldRefund.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: oldTimeLock})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 3}
	newRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 1},
		Sequence:         oldTimeLock - spark.TimeLockInterval,
	})
	newRefund.AddTxOut(&wire.TxOut{Value: nodeTx.TxOut[0].Value, PkScript: p2tr})
	newRefund.AddTxOut(common.EphemeralAnchorOutput())
	newRefundBytes := mustSerializeTx(t, newRefund)

	newDirectFromCpfpRefund := &wire.MsgTx{Version: 3}
	newDirectFromCpfpRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0},
		Sequence:         oldTimeLock - spark.TimeLockInterval + spark.DirectTimelockOffset,
	})
	newDirectFromCpfpRefund.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(nodeTx.TxOut[0].Value), PkScript: p2tr})
	newDirectFromCpfpRefundBytes := mustSerializeTx(t, newDirectFromCpfpRefund)

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
	leafDirectFromCpfpRefundMap := map[string][]byte{
		leaf.ID.String(): newDirectFromCpfpRefundBytes,
	}

	h := NewBaseTransferHandler(config)
	_, _, err = h.createTransfer(
		ctx,
		uuid.New(),
		nil,
		st.TransferTypeTransfer,
		time.Now().Add(10*time.Minute),
		senderPub,
		receiverPub,
		leafCpfpRefundMap,
		map[string][]byte{},
		leafDirectFromCpfpRefundMap,
		nil,
		TransferRoleCoordinator,
		false,
		"",
		uuid.Nil,
		nil,
	)
	if err == nil {
		t.Fatalf("expected error for wrong outpoint, got nil")
	}
}

// Test that the Swap V3 counter transfer fails if the total value of the leaves does not match the total value of the primary transfer.
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
	_, err = client.TransferSender.Create().SetTransferID(primaryTransfer.ID).SetIdentityPubkey(senderPub).Save(ctx)
	require.NoError(t, err)
	_, err = client.TransferReceiver.Create().
		SetTransferID(primaryTransfer.ID).
		SetIdentityPubkey(receiverPub).
		SetStatus(st.TransferReceiverStatusInitiated).
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

	nodeTx := &wire.MsgTx{Version: 3}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)
	nodeHash := nodeTx.TxHash()

	const oldTimeLock uint32 = 600
	oldRefund := &wire.MsgTx{Version: 3}
	oldRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock,
	})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 3}
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
		transferID,
		nil,
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
		nil,
	)
	require.Error(t, err)
	expectedErrSubstring := "does not match counter transfer amount"
	require.ErrorContains(t, err, expectedErrSubstring)
}

func TestCreateTransfer_CounterSwapV3_FailsWithMismatchedNetwork(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Primary transfer: Alice -> Bob on regtest.
	alicePub := keys.GeneratePrivateKey().Public()
	bobPub := keys.GeneratePrivateKey().Public()

	primaryTransfer, err := client.Transfer.Create().
		SetSenderIdentityPubkey(alicePub).
		SetReceiverIdentityPubkey(bobPub).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetType(st.TransferTypePrimarySwapV3).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.TransferSender.Create().SetTransferID(primaryTransfer.ID).SetIdentityPubkey(alicePub).Save(ctx)
	require.NoError(t, err)
	_, err = client.TransferReceiver.Create().
		SetTransferID(primaryTransfer.ID).
		SetIdentityPubkey(bobPub).
		SetStatus(st.TransferReceiverStatusInitiated).
		Save(ctx)
	require.NoError(t, err)

	// Counter transfer leaf has the same amount and reversed parties, but a different network.
	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Signet).
		SetOwnerIdentityPubkey(bobPub).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	p2tr, err := common.P2TRScriptFromPubKey(alicePub)
	require.NoError(t, err)

	nodeTx := &wire.MsgTx{Version: 3}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)
	nodeHash := nodeTx.TxHash()

	const oldTimeLock uint32 = 600
	oldRefund := &wire.MsgTx{Version: 3}
	oldRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock,
	})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 3}
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

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(bobPub).
		SetOwnerSigningPubkey(bobPub).
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
	_, _, err = h.createTransfer(
		ctx,
		uuid.New(),
		nil,
		st.TransferTypeCounterSwapV3,
		time.Now().Add(10*time.Minute),
		bobPub,
		alicePub,
		leafCpfpRefundMap,
		map[string][]byte{},
		map[string][]byte{},
		nil,
		TransferRoleCoordinator,
		false,
		"",
		primaryTransfer.ID,
		nil,
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "does not match counter transfer network")
}

// Test that the Swap V3 counter transfer fails if the sender/receiver don't match the primary transfer parties in reverse.
func TestCreateTransfer_CounterSwapV3_FailsWithMismatchedParties(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Primary transfer: Alice -> Bob
	alicePub := keys.GeneratePrivateKey().Public()
	bobPub := keys.GeneratePrivateKey().Public()
	charliePub := keys.GeneratePrivateKey().Public()

	// Create primary swap transfer: Alice -> Bob
	primaryTransfer, err := client.Transfer.Create().
		SetSenderIdentityPubkey(alicePub).
		SetReceiverIdentityPubkey(bobPub).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetType(st.TransferTypePrimarySwapV3).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.TransferSender.Create().SetTransferID(primaryTransfer.ID).SetIdentityPubkey(alicePub).Save(ctx)
	require.NoError(t, err)
	_, err = client.TransferReceiver.Create().
		SetTransferID(primaryTransfer.ID).
		SetIdentityPubkey(bobPub).
		SetStatus(st.TransferReceiverStatusInitiated).
		Save(ctx)
	require.NoError(t, err)

	// Create entities needed to create a counter transfer
	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(charliePub).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	p2tr, err := common.P2TRScriptFromPubKey(charliePub)
	require.NoError(t, err)

	nodeTx := &wire.MsgTx{Version: 3}
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}, Sequence: 0})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())
	nodeBytes := mustSerializeTx(t, nodeTx)
	nodeHash := nodeTx.TxHash()

	const oldTimeLock uint32 = 600
	oldRefund := &wire.MsgTx{Version: 3}
	oldRefund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: nodeHash, Index: 0},
		Sequence:         oldTimeLock,
	})
	oldRefund.AddTxOut(common.EphemeralAnchorOutput())
	oldRefundBytes := mustSerializeTx(t, oldRefund)

	newRefund := &wire.MsgTx{Version: 3}
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

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(charliePub).
		SetOwnerSigningPubkey(charliePub).
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
	expiry := time.Now().Add(10 * time.Minute)

	tests := []struct {
		name              string
		senderPubKey      keys.Public
		receiverPubKey    keys.Public
		expectedErrSubstr string
	}{
		{
			name:              "sender mismatch",
			senderPubKey:      charliePub, // Wrong sender (should be Bob)
			receiverPubKey:    alicePub,   // Correct receiver (Alice)
			expectedErrSubstr: "counter transfer sender must be the primary transfer receiver",
		},
		{
			name:              "receiver mismatch",
			senderPubKey:      bobPub,     // Correct sender (Bob)
			receiverPubKey:    charliePub, // Wrong receiver (should be Alice)
			expectedErrSubstr: "counter transfer receiver must be the primary transfer sender",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Try to create Swap V3 counter transfer with mismatched parties
			_, _, err = h.createTransfer(
				ctx,
				uuid.New(),
				nil,
				st.TransferTypeCounterSwapV3,
				expiry,
				tt.senderPubKey,
				tt.receiverPubKey,
				leafCpfpRefundMap,
				map[string][]byte{},
				map[string][]byte{},
				nil,
				TransferRoleCoordinator,
				false,
				"",
				primaryTransfer.ID,
				nil,
			)

			require.Error(t, err)
			require.ErrorContains(t, err, tt.expectedErrSubstr)
		})
	}
}

func TestCancelTransferInternal_PreimageSwap(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderPub := keys.GeneratePrivateKey().Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	// Cancellation must succeed even when PreimageRequest is PREIMAGE_SHARED.
	// This handles the wrong-hash scenario: InitiatePreimageSwap sets PREIMAGE_SHARED
	// during setup, then rolls back after hash mismatch and cancels via gossip.
	// ReturnStuckTransfers handles its own race via atomic update (n==0 check).
	t.Run("allows cancellation when preimage already shared", func(t *testing.T) {
		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderKeyTweakPending).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypePreimageSwap).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.PreimageRequest.Create().
			SetPaymentHash([]byte("test_hash_shared_32_bytes_long__")).
			SetStatus(st.PreimageRequestStatusPreimageShared).
			SetReceiverIdentityPubkey(receiverPub).
			SetTransfers(transfer).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.NoError(t, err)

		// Verify transfer status was changed to RETURNED
		updated, err := client.Transfer.Query().Where(enttransfer.ID(transfer.ID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, st.TransferStatusReturned, updated.Status)
	})

	t.Run("allows cancellation when preimage not shared", func(t *testing.T) {
		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderKeyTweakPending).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypePreimageSwap).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.PreimageRequest.Create().
			SetPaymentHash([]byte("test_hash_waiting_32_bytes_long_")).
			SetStatus(st.PreimageRequestStatusWaitingForPreimage).
			SetReceiverIdentityPubkey(receiverPub).
			SetTransfers(transfer).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.NoError(t, err)

		// Verify transfer status was changed to RETURNED
		updated, err := client.Transfer.Query().Where(enttransfer.ID(transfer.ID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, st.TransferStatusReturned, updated.Status)
	})

	t.Run("allows cancellation of non-preimage-swap transfers", func(t *testing.T) {
		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderKeyTweakPending).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.NoError(t, err)

		// Verify transfer status was changed to RETURNED
		updated, err := client.Transfer.Query().Where(enttransfer.ID(transfer.ID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, st.TransferStatusReturned, updated.Status)
	})
}

func TestCancelTransferInternal_UpdatesReceiverStatus(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderPub := keys.GeneratePrivateKey().Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	t.Run("cancellation sets receiver to cancelled", func(t *testing.T) {
		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderInitiated).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		receiver, err := client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPub).
			SetStatus(st.TransferReceiverStatusInitiated).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.NoError(t, err)

		updated, err := client.Transfer.Query().Where(enttransfer.ID(transfer.ID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, st.TransferStatusReturned, updated.Status)

		updatedReceiver, err := client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		require.Equal(t, st.TransferReceiverStatusCancelled, updatedReceiver.Status)
	})

	t.Run("cancellation skips already-cancelled receiver", func(t *testing.T) {
		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderInitiated).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		receiver, err := client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPub).
			SetStatus(st.TransferReceiverStatusCancelled).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.NoError(t, err)

		updatedReceiver, err := client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		require.Equal(t, st.TransferReceiverStatusCancelled, updatedReceiver.Status)
	})

	t.Run("cancellation works with no receiver", func(t *testing.T) {
		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderInitiated).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.NoError(t, err)

		updated, err := client.Transfer.Query().Where(enttransfer.ID(transfer.ID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, st.TransferStatusReturned, updated.Status)
	})

	t.Run("cancellation errors on unexpected receiver status", func(t *testing.T) {
		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderInitiated).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPub).
			SetStatus(st.TransferReceiverStatusRefundSigned).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected status")
	})

	t.Run("cancellation cancels multiple receivers", func(t *testing.T) {
		receiverPub2 := keys.GeneratePrivateKey().Public()

		transfer, err := client.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderInitiated).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypeTransfer).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		receiver1, err := client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPub).
			SetStatus(st.TransferReceiverStatusInitiated).
			Save(ctx)
		require.NoError(t, err)

		receiver2, err := client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPub2).
			SetStatus(st.TransferReceiverStatusInitiated).
			Save(ctx)
		require.NoError(t, err)

		h := NewBaseTransferHandler(config)
		err = h.CancelTransferInternal(ctx, transfer.ID)
		require.NoError(t, err)

		updatedR1, err := client.TransferReceiver.Get(ctx, receiver1.ID)
		require.NoError(t, err)
		require.Equal(t, st.TransferReceiverStatusCancelled, updatedR1.Status)

		updatedR2, err := client.TransferReceiver.Get(ctx, receiver2.ID)
		require.NoError(t, err)
		require.Equal(t, st.TransferReceiverStatusCancelled, updatedR2.Status)
	})
}

// TestCancelTransfer_DoesNotReviveExitedLeaf is a regression test for SP-3049.
// Before the fix, cancelTransferUnlockLeaves unconditionally reset every leaf
// of the cancelled transfer to AVAILABLE — including leaves that had already
// transitioned to a terminal status (e.g. EXITED after the refund tx confirmed
// on-chain). That allowed a sender to create a second transfer from an
// outpoint whose UTXO had already been spent.
func TestCancelTransfer_DoesNotReviveExitedLeaf(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderPub := keys.GeneratePrivateKey().Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPub).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

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

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(keyshare).
		SetValue(1000).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(senderPub).
		SetOwnerSigningPubkey(senderPub).
		SetRawTx(createTestTxBytes(t, 3000)).
		SetRawRefundTx(createTestTxBytes(t, 3100)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transfer, err := client.Transfer.Create().
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiverPub).
		SetStatus(st.TransferStatusSenderInitiated).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetType(st.TransferTypeTransfer).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 4000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 4001)).
		Save(ctx)
	require.NoError(t, err)

	// Simulate the refund tx confirming on-chain: MarkExitingNodes promotes
	// the leaf to EXITED while the transfer is still SENDER_INITIATED.
	_, err = leaf.Update().SetStatus(st.TreeNodeStatusExited).Save(ctx)
	require.NoError(t, err)

	h := NewBaseTransferHandler(config)
	err = h.CancelTransferInternal(ctx, transfer.ID)
	require.NoError(t, err)

	// Cancel must complete cleanly — transfer marked RETURNED.
	updatedTransfer, err := client.Transfer.Query().Where(enttransfer.ID(transfer.ID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReturned, updatedTransfer.Status)

	// Critically: the on-chain leaf must NOT be revived. Reviving it would let
	// the sender start a second transfer from an already-spent outpoint.
	updatedLeaf, err := client.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusExited, updatedLeaf.Status,
		"cancel must not revive a leaf whose refund tx already confirmed on-chain (SP-3049)")
}

func TestValidateTransferPackage_DuplicateLeafID(t *testing.T) {
	config := sparktesting.TestConfig(t)
	h := NewBaseTransferHandler(config)
	leafID := uuid.New().String()

	pkg := &pbspark.TransferPackage{
		KeyTweakPackage: map[string][]byte{"so-0": {1, 2, 3}},
		LeavesToSend: []*pbspark.UserSignedTxSigningJob{
			{LeafId: leafID, RawTx: []byte{1}},
			{LeafId: leafID, RawTx: []byte{2}},
		},
	}

	_, err := h.ValidateTransferPackage(t.Context(), uuid.New(), pkg, keys.GeneratePrivateKey().Public(), false)
	require.Error(t, err)
	require.ErrorContains(t, err, "duplicate leaf id in LeavesToSend")
}

func TestValidateTransferPackage_OrphanDirectLeaf(t *testing.T) {
	config := sparktesting.TestConfig(t)
	h := NewBaseTransferHandler(config)
	leafID := uuid.New().String()

	pkg := &pbspark.TransferPackage{
		KeyTweakPackage: map[string][]byte{"so-0": {1, 2, 3}},
		LeavesToSend: []*pbspark.UserSignedTxSigningJob{
			{LeafId: leafID, RawTx: []byte{1}},
		},
		DirectLeavesToSend: []*pbspark.UserSignedTxSigningJob{
			{LeafId: uuid.New().String(), RawTx: []byte{2}}, // different ID
		},
		DirectFromCpfpLeavesToSend: []*pbspark.UserSignedTxSigningJob{
			{LeafId: leafID, RawTx: []byte{3}},
		},
	}

	_, err := h.ValidateTransferPackage(t.Context(), uuid.New(), pkg, keys.GeneratePrivateKey().Public(), false)
	require.Error(t, err)
	require.ErrorContains(t, err, "orphan leaf in DirectLeavesToSend")
}

func TestValidateTransferPackage_MissingDirectFromCpfpLeaves(t *testing.T) {
	config := sparktesting.TestConfig(t)
	h := NewBaseTransferHandler(config)
	leafID := uuid.New().String()

	pkg := &pbspark.TransferPackage{
		KeyTweakPackage: map[string][]byte{"so-0": {1, 2, 3}},
		LeavesToSend: []*pbspark.UserSignedTxSigningJob{
			{LeafId: leafID, RawTx: []byte{1}},
		},
	}

	// When requireDirectFromCpfpLeaves is true, missing DirectFromCpfpLeavesToSend should fail.
	_, err := h.ValidateTransferPackage(t.Context(), uuid.New(), pkg, keys.GeneratePrivateKey().Public(), true)
	require.Error(t, err)
	require.ErrorContains(t, err, "mismatched number of leaves")

	// When requireDirectFromCpfpLeaves is false (swap), missing DirectFromCpfpLeavesToSend is allowed.
	// The error should NOT be about mismatched leaves (it may fail later on other validation).
	_, err = h.ValidateTransferPackage(t.Context(), uuid.New(), pkg, keys.GeneratePrivateKey().Public(), false)
	if err != nil {
		require.NotContains(t, err.Error(), "mismatched number of leaves")
	}
}

func TestVerifySenderKeyTweakProofsMatch_Match(t *testing.T) {
	leafID := uuid.New().String()
	proofs := [][]byte{{1, 2, 3}, {4, 5, 6}}

	keyTweakMap := map[string]*pbspark.SendLeafKeyTweak{
		leafID: {
			LeafId:           leafID,
			SecretShareTweak: &pbspark.SecretShare{Proofs: proofs},
		},
	}
	senderProofs := map[string]*pbspark.SecretProof{
		leafID: {Proofs: proofs},
	}

	err := verifySenderKeyTweakProofsMatch(keyTweakMap, senderProofs)
	require.NoError(t, err)
}

func TestVerifySenderKeyTweakProofsMatch_NilInputs(t *testing.T) {
	err := verifySenderKeyTweakProofsMatch(nil, map[string]*pbspark.SecretProof{})
	require.ErrorContains(t, err, "must not be nil")

	err = verifySenderKeyTweakProofsMatch(map[string]*pbspark.SendLeafKeyTweak{}, nil)
	require.ErrorContains(t, err, "must not be nil")
}

func TestVerifySenderKeyTweakProofsMatch_CountMismatch(t *testing.T) {
	leafID := uuid.New().String()

	keyTweakMap := map[string]*pbspark.SendLeafKeyTweak{
		leafID: {
			LeafId:           leafID,
			SecretShareTweak: &pbspark.SecretShare{Proofs: [][]byte{{1}}},
		},
	}

	err := verifySenderKeyTweakProofsMatch(keyTweakMap, map[string]*pbspark.SecretProof{})
	require.ErrorContains(t, err, "sender key tweak proof count mismatch")
}

func TestVerifySenderKeyTweakProofsMatch_MissingProof(t *testing.T) {
	leafID1 := uuid.New().String()
	leafID2 := uuid.New().String()

	keyTweakMap := map[string]*pbspark.SendLeafKeyTweak{
		leafID1: {
			LeafId:           leafID1,
			SecretShareTweak: &pbspark.SecretShare{Proofs: [][]byte{{1}}},
		},
	}
	senderProofs := map[string]*pbspark.SecretProof{
		leafID2: {Proofs: [][]byte{{1}}},
	}

	err := verifySenderKeyTweakProofsMatch(keyTweakMap, senderProofs)
	require.ErrorContains(t, err, "sender key tweak proof missing for leaf")
}

func TestVerifySenderKeyTweakProofsMatch_Mismatch(t *testing.T) {
	leafID := uuid.New().String()

	keyTweakMap := map[string]*pbspark.SendLeafKeyTweak{
		leafID: {
			LeafId:           leafID,
			SecretShareTweak: &pbspark.SecretShare{Proofs: [][]byte{{1, 2, 3}}},
		},
	}
	senderProofs := map[string]*pbspark.SecretProof{
		leafID: {Proofs: [][]byte{{9, 9, 9}}},
	}

	err := verifySenderKeyTweakProofsMatch(keyTweakMap, senderProofs)
	require.ErrorContains(t, err, "sender key tweak proof mismatch for leaf")
}

func TestCreateTransferEdgeRows_DenormalizeTransferTypeFromParent(t *testing.T) {
	cases := []st.TransferType{
		st.TransferTypeTransfer,
		st.TransferTypeCounterSwap,
		st.TransferTypeSwap,
		st.TransferTypePreimageSwap,
		st.TransferTypeCooperativeExit,
		st.TransferTypeUtxoSwap,
		st.TransferTypePrimarySwapV3,
		st.TransferTypeCounterSwapV3,
	}
	for _, parentType := range cases {
		t.Run(string(parentType), func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			client, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			senderPub := keys.GeneratePrivateKey().Public()
			receiverPub := keys.GeneratePrivateKey().Public()

			tr, err := client.Transfer.Create().
				SetSenderIdentityPubkey(senderPub).
				SetReceiverIdentityPubkey(receiverPub).
				SetStatus(st.TransferStatusCompleted).
				SetType(parentType).
				SetNetwork(btcnetwork.Regtest).
				SetTotalValue(1000).
				SetExpiryTime(time.Now().Add(10 * time.Minute)).
				Save(ctx)
			require.NoError(t, err)

			sender, err := createTransferSender(ctx, client, tr, senderPub)
			require.NoError(t, err)
			require.Equal(t, parentType, sender.TransferType, "sender transfer_type should match parent")
			require.True(t, sender.CreateTime.Equal(tr.CreateTime), "sender create_time should match parent")

			receiver, err := createTransferReceiver(ctx, client, tr, receiverPub, st.TransferReceiverStatusCompleted)
			require.NoError(t, err)
			require.Equal(t, parentType, receiver.TransferType, "receiver transfer_type should match parent")
			require.True(t, receiver.CreateTime.Equal(tr.CreateTime), "receiver create_time should match parent")
		})
	}
}
