package backfill

import (
	"bytes"
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/transfersender"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()
	m.Run()
}

func TestMapTransferToReceiverStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		transferStatus st.TransferStatus
		expectedStatus st.TransferReceiverStatus
	}{
		{st.TransferStatusSenderInitiated, st.TransferReceiverStatusSenderInitiated},
		{st.TransferStatusSenderInitiatedCoordinator, st.TransferReceiverStatusSenderInitiated},
		{st.TransferStatusSenderKeyTweakPending, st.TransferReceiverStatusSenderInitiated},
		{st.TransferStatusApplyingSenderKeyTweak, st.TransferReceiverStatusSenderInitiated},
		{st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusSenderInitiated},
		{st.TransferStatusReceiverKeyTweaked, st.TransferReceiverStatusKeyTweaked},
		{st.TransferStatusReceiverKeyTweakLocked, st.TransferReceiverStatusKeyTweakLocked},
		{st.TransferStatusReceiverKeyTweakApplied, st.TransferReceiverStatusKeyTweakApplied},
		{st.TransferStatusReceiverRefundSigned, st.TransferReceiverStatusRefundSigned},
		{st.TransferStatusCompleted, st.TransferReceiverStatusCompleted},
		{st.TransferStatusExpired, st.TransferReceiverStatusCancelled},
		{st.TransferStatusReturned, st.TransferReceiverStatusCancelled},
	}

	for _, tt := range tests {
		t.Run(string(tt.transferStatus), func(t *testing.T) {
			result := MapTransferToReceiverStatus(tt.transferStatus)
			assert.Equal(t, tt.expectedStatus, result)
		})
	}
}

func buildTestTxBytes(t *testing.T, value int64) []byte {
	t.Helper()
	tx := wire.NewMsgTx(3)
	input := wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}, nil, nil)
	input.Sequence = 2000
	tx.AddTxIn(input)
	pkScript, err := txscript.NewScriptBuilder().AddOp(txscript.OP_TRUE).Script()
	require.NoError(t, err)
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return buf.Bytes()
}

// createTestEntitiesForBackfill creates a Transfer with associated tree/leaf entities
// using the context-backed client so all data is visible within the same transaction.
func createTestEntitiesForBackfill(
	t *testing.T,
	ctx context.Context,
	rng *rand.ChaCha8,
	status st.TransferStatus,
	completionTime *time.Time,
	numLeaves int,
) *ent.Transfer {
	t.Helper()

	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transferCreate := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPubKey).
		SetReceiverIdentityPubkey(receiverPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour))
	if completionTime != nil {
		transferCreate = transferCreate.SetCompletionTime(*completionTime)
	}
	transfer, err := transferCreate.Save(ctx)
	require.NoError(t, err)

	baseTxid := st.NewRandomTxIDForTesting(t)
	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerPubKey).
		SetBaseTxid(baseTxid).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

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

	for i := range numLeaves {
		verifyPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		leafOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		leafOwnerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

		leaf, err := client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusTransferLocked).
			SetTree(tree).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(signingKeyshare).
			SetValue(500).
			SetVerifyingPubkey(verifyPubKey).
			SetOwnerIdentityPubkey(leafOwnerPubKey).
			SetOwnerSigningPubkey(leafOwnerSigningPubKey).
			SetRawTx(buildTestTxBytes(t, int64(1000+i))).
			SetRawRefundTx(buildTestTxBytes(t, int64(2000+i))).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		_, err = client.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(buildTestTxBytes(t, int64(3000+i))).
			SetIntermediateRefundTx(buildTestTxBytes(t, int64(4000+i))).
			Save(ctx)
		require.NoError(t, err)
	}

	return transfer
}

func TestBackfillMimoTransfers(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{})

	t.Run("no transfers to backfill", func(t *testing.T) {
		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 0, result.TransfersCreated)
		assert.Equal(t, 0, result.ReceiverStatusesUpdated)
	})

	t.Run("backfills sender initiated transfer", func(t *testing.T) {
		transfer := createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusSenderInitiated, nil, 1)

		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 1, result.TransfersCreated)

		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		senders, err := dbClient.TransferSender.Query().
			Where(transfersender.TransferIDEQ(transfer.ID)).All(ctx)
		require.NoError(t, err)
		require.Len(t, senders, 1)
		assert.Equal(t, transfer.SenderIdentityPubkey, senders[0].IdentityPubkey)

		receivers, err := dbClient.TransferReceiver.Query().
			Where(transferreceiver.TransferIDEQ(transfer.ID)).All(ctx)
		require.NoError(t, err)
		require.Len(t, receivers, 1)
		assert.Equal(t, transfer.ReceiverIdentityPubkey, receivers[0].IdentityPubkey)
		assert.Equal(t, st.TransferReceiverStatusSenderInitiated, receivers[0].Status)
		assert.True(t, receivers[0].CompletionTime.IsZero())

		leaves, err := dbClient.TransferLeaf.Query().All(ctx)
		require.NoError(t, err)
		for _, leaf := range leaves {
			if leaf.TransferSenderID != nil {
				assert.Equal(t, senders[0].ID, *leaf.TransferSenderID)
			}
		}
	})

	t.Run("backfills completed transfer with completion_time", func(t *testing.T) {
		completionTime := time.Now().Add(-1 * time.Hour)
		transfer := createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusCompleted, &completionTime, 2)

		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 1, result.TransfersCreated)

		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		receivers, err := dbClient.TransferReceiver.Query().
			Where(transferreceiver.TransferIDEQ(transfer.ID)).All(ctx)
		require.NoError(t, err)
		require.Len(t, receivers, 1)
		assert.Equal(t, st.TransferReceiverStatusCompleted, receivers[0].Status)
		assert.NotNil(t, receivers[0].CompletionTime)
	})

	t.Run("backfills expired transfer as cancelled", func(t *testing.T) {
		transfer := createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusExpired, nil, 1)

		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 1, result.TransfersCreated)

		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		receivers, err := dbClient.TransferReceiver.Query().
			Where(transferreceiver.TransferIDEQ(transfer.ID)).All(ctx)
		require.NoError(t, err)
		require.Len(t, receivers, 1)
		assert.Equal(t, st.TransferReceiverStatusCancelled, receivers[0].Status)
	})

	t.Run("skips already backfilled transfers", func(t *testing.T) {
		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 0, result.TransfersCreated)
	})

	t.Run("respects batch size", func(t *testing.T) {
		createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusSenderInitiated, nil, 1)
		createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusCompleted, nil, 1)
		createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusReceiverKeyTweaked, nil, 1)

		result, err := BackfillMimoTransfers(ctx, nil, 2)
		require.NoError(t, err)
		assert.Equal(t, 2, result.TransfersCreated)

		result, err = BackfillMimoTransfers(ctx, nil, 2)
		require.NoError(t, err)
		assert.Equal(t, 1, result.TransfersCreated)
	})

	t.Run("skips transfers with UNSPECIFIED network", func(t *testing.T) {
		client, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		unspecifiedTransfer, err := client.Transfer.Create().
			SetNetwork(btcnetwork.Unspecified).
			SetStatus(st.TransferStatusReturned).
			SetType(st.TransferTypeTransfer).
			SetSenderIdentityPubkey(senderPubKey).
			SetReceiverIdentityPubkey(receiverPubKey).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(24 * time.Hour)).
			Save(ctx)
		require.NoError(t, err)

		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 0, result.TransfersCreated)

		senders, err := client.TransferSender.Query().
			Where(transfersender.TransferIDEQ(unspecifiedTransfer.ID)).All(ctx)
		require.NoError(t, err)
		assert.Empty(t, senders, "UNSPECIFIED network transfers should not be backfilled")
	})
}

func TestBackfillMimoTransfers_SyncsReceiverStatuses(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{42})

	t.Run("updates stale receiver status to match terminal transfer", func(t *testing.T) {
		// Create a transfer at SenderInitiated so the backfill creates the
		// receiver with SenderInitiated status.
		transfer := createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusSenderInitiated, nil, 1)

		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 1, result.TransfersCreated)
		assert.Equal(t, 0, result.ReceiverStatusesUpdated)

		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		// Simulate the gap: advance the Transfer to a terminal status without
		// updating the receiver (mimicking the period before dual-write was enabled).
		_, err = transfer.Update().
			SetStatus(st.TransferStatusExpired).
			Save(ctx)
		require.NoError(t, err)

		// Run again — the receiver status sync should catch the mismatch.
		result, err = BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 0, result.TransfersCreated)
		assert.Equal(t, 1, result.ReceiverStatusesUpdated)

		receivers, err := dbClient.TransferReceiver.Query().
			Where(transferreceiver.TransferIDEQ(transfer.ID)).All(ctx)
		require.NoError(t, err)
		require.Len(t, receivers, 1)
		assert.Equal(t, st.TransferReceiverStatusCancelled, receivers[0].Status)
	})

	t.Run("sets completion time when syncing to completed", func(t *testing.T) {
		transfer := createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusSenderInitiated, nil, 1)

		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 1, result.TransfersCreated)

		completionTime := time.Now().Add(-30 * time.Minute)
		_, err = transfer.Update().
			SetStatus(st.TransferStatusCompleted).
			SetCompletionTime(completionTime).
			Save(ctx)
		require.NoError(t, err)

		result, err = BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 1, result.ReceiverStatusesUpdated)

		dbClient, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		receivers, err := dbClient.TransferReceiver.Query().
			Where(transferreceiver.TransferIDEQ(transfer.ID)).All(ctx)
		require.NoError(t, err)
		require.Len(t, receivers, 1)
		assert.Equal(t, st.TransferReceiverStatusCompleted, receivers[0].Status)
		assert.False(t, receivers[0].CompletionTime.IsZero())
	})

	t.Run("skips receivers already in sync", func(t *testing.T) {
		// All previously synced receivers are now in terminal states (Completed/KeyTweaked).
		// Creating a fresh transfer already at Completed so receiver is created correctly.
		completionTime := time.Now()
		createTestEntitiesForBackfill(t, ctx, rng, st.TransferStatusCompleted, &completionTime, 1)

		result, err := BackfillMimoTransfers(ctx, nil, 1000)
		require.NoError(t, err)
		assert.Equal(t, 1, result.TransfersCreated)
		assert.Equal(t, 0, result.ReceiverStatusesUpdated)
	})
}
