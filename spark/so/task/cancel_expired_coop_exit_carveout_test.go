package task

import (
	"bytes"
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findCancelExpiredTransfersTask(t *testing.T) ScheduledTaskSpec {
	t.Helper()
	for _, scheduledTask := range AllScheduledTasks() {
		if scheduledTask.Name == "cancel_expired_transfers" {
			return scheduledTask
		}
	}
	t.Fatal("cancel_expired_transfers task not found")
	return ScheduledTaskSpec{}
}

func coopExitCarveoutRawTxBytes(t *testing.T, sequence uint32) []byte {
	t.Helper()
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{},
		Sequence:         sequence,
	})
	tx.AddTxOut(&wire.TxOut{
		Value:    1000,
		PkScript: []byte{txscript.OP_TRUE},
	})
	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return buf.Bytes()
}

func createCoopExitCarveoutSigningKeyshare(t *testing.T, ctx context.Context, rng *rand.ChaCha8, client *ent.Client) *ent.SigningKeyshare {
	t.Helper()
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	pubSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"operator1": pubSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	return signingKeyshare
}

func createCoopExitCarveoutTree(t *testing.T, ctx context.Context, ownerIdentityPubKey keys.Public, client *ent.Client) *ent.Tree {
	t.Helper()
	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return tree
}

func createCoopExitCarveoutTreeNode(t *testing.T, ctx context.Context, rng *rand.ChaCha8, client *ent.Client, tree *ent.Tree, keyshare *ent.SigningKeyshare) *ent.TreeNode {
	t.Helper()
	rawTx := coopExitCarveoutRawTxBytes(t, 1)
	refundTx := coopExitCarveoutRawTxBytes(t, 2)
	directRefundTx := coopExitCarveoutRawTxBytes(t, 3)
	directFromCpfpRefundTx := coopExitCarveoutRawTxBytes(t, 4)

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(keyshare).
		SetValue(1000).
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(tree.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(directRefundTx).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return leaf
}

func createCoopExitCarveoutTransferWithReceiver(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	senderIdentityPubKey keys.Public,
	receiverIdentityPubKey keys.Public,
) (*ent.Transfer, *ent.TransferReceiver) {
	t.Helper()

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(st.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(-1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	receiver, err := client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverIdentityPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	return transfer, receiver
}

func TestCancelExpiredTransfers_DoesNotReturnSenderKeyTweakPendingCoopExitTransfers(t *testing.T) {
	t.Parallel()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	rng := rand.NewChaCha8([32]byte{53})
	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPubkey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createCoopExitCarveoutSigningKeyshare(t, ctx, rng, client)
	tree := createCoopExitCarveoutTree(t, ctx, senderIdentityPrivKey.Public(), client)
	leaf := createCoopExitCarveoutTreeNode(t, ctx, rng, client, tree, keyshare)
	transfer, receiver := createCoopExitCarveoutTransferWithReceiver(
		t,
		ctx,
		client,
		senderIdentityPrivKey.Public(),
		receiverIdentityPubkey,
	)

	_, err := client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetTransferReceiver(receiver).
		SetPreviousRefundTx(coopExitCarveoutRawTxBytes(t, 5)).
		SetIntermediateRefundTx(coopExitCarveoutRawTxBytes(t, 6)).
		SetKeyTweak([]byte("sender-key-tweak")).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.CooperativeExit.Create().
		SetTransfer(transfer).
		SetExitTxid(st.NewRandomTxIDForTesting(t)).
		Save(ctx)
	require.NoError(t, err)

	cancelTask := findCancelExpiredTransfersTask(t)
	require.NoError(t, cancelTask.RunOnce(ctx, cfg, client, nil, knobs.NewFixedKnobs(nil)))

	updatedTransfer, err := client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, updatedTransfer.Status)

	updatedReceiver, err := client.TransferReceiver.Get(ctx, receiver.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferReceiverStatusInitiated, updatedReceiver.Status)

	updatedLeaf, err := client.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusTransferLocked, updatedLeaf.Status)
}
