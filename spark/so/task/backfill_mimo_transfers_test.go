package task

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/transfersender"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

// --- Cursor serialization tests (acceptable lower-level: serialization contract) ---

func TestParseBackfillCursor_RoundTrip(t *testing.T) {
	t.Parallel()
	original := backfillCursorState{
		UpdateTime: time.Date(2026, time.March, 15, 12, 0, 0, 0, time.UTC),
		ID:         uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef"),
	}
	serialized := original.String()
	parsed, ok := parseBackfillCursor(serialized)
	require.True(t, ok)
	assert.Equal(t, original.UpdateTime.UnixMicro(), parsed.UpdateTime.UnixMicro())
	assert.Equal(t, original.ID, parsed.ID)
}

func TestParseBackfillCursor_InvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"empty string", "", false},
		{"no colon", "12345", false},
		{"non-numeric timestamp", "abc:01234567-89ab-cdef-0123-456789abcdef", false},
		{"invalid uuid", "12345:not-a-uuid", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := parseBackfillCursor(tc.input)
			assert.Equal(t, tc.valid, ok)
		})
	}
}

func TestBackfillCursorKey_IncludesOperatorIndex(t *testing.T) {
	t.Parallel()
	key0 := backfillCursorKey(0)
	key1 := backfillCursorKey(1)
	assert.Contains(t, key0, ":0")
	assert.Contains(t, key1, ":1")
	assert.NotEqual(t, key0, key1)
}

// --- Backfill task tests ---
// These tests are NOT parallel because they share the global backfillMu mutex.

func buildBackfillTestTx(t *testing.T, value int64) []byte {
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

// createTransferWithLeafs creates a Transfer with the required entity graph
// (Tree -> TreeNode -> TransferLeaf) but no TransferSender or TransferReceiver,
// simulating a pre-MIMO transfer that needs backfilling.
func createTransferWithLeafs(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	status st.TransferStatus,
	createTime time.Time,
	completionTime *time.Time,
	numLeafs int,
) *ent.Transfer {
	t.Helper()

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	ownerKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	transferCreate := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetCreateTime(createTime)
	if completionTime != nil {
		transferCreate = transferCreate.SetCompletionTime(*completionTime)
	}
	tr := transferCreate.SaveX(ctx)

	baseTxid := st.NewRandomTxIDForTesting(t)
	tree := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerKey).
		SetBaseTxid(baseTxid).
		SetVout(0).
		SaveX(ctx)

	keysharePriv := keys.MustGeneratePrivateKeyFromRand(rand.Reader)
	pubSharePriv := keys.MustGeneratePrivateKeyFromRand(rand.Reader)
	keyshare := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keysharePriv).
		SetPublicShares(map[string]keys.Public{"op1": pubSharePriv.Public()}).
		SetPublicKey(keysharePriv.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		SaveX(ctx)

	for i := range numLeafs {
		verifyKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
		leafOwner := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
		leafSigner := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

		leaf := client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusTransferLocked).
			SetTree(tree).
			SetNetwork(btcnetwork.Regtest).
			SetSigningKeyshare(keyshare).
			SetValue(500).
			SetVerifyingPubkey(verifyKey).
			SetOwnerIdentityPubkey(leafOwner).
			SetOwnerSigningPubkey(leafSigner).
			SetRawTx(buildBackfillTestTx(t, int64(1000+i))).
			SetRawRefundTx(buildBackfillTestTx(t, int64(2000+i))).
			SetVout(0).
			SaveX(ctx)

		client.TransferLeaf.Create().
			SetTransfer(tr).
			SetLeaf(leaf).
			SetPreviousRefundTx(buildBackfillTestTx(t, int64(3000+i))).
			SetIntermediateRefundTx(buildBackfillTestTx(t, int64(4000+i))).
			SaveX(ctx)
	}

	return tr
}

func getBackfillTask(t *testing.T) ScheduledTaskSpec {
	t.Helper()
	for _, task := range AllScheduledTasks() {
		if task.Name == "backfill_mimo_transfers" {
			return task
		}
	}
	t.Fatal("backfill_mimo_transfers task not found in AllScheduledTasks")
	return ScheduledTaskSpec{}
}

// TryLock concurrency contract (acceptable lower-level test).
func TestBackfillMimoTransfers_TryLock(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)

	backfillMu.Lock()
	defer backfillMu.Unlock()

	result, err := backfillMimoTransfers(ctx, cfg, sessionCtx.Client, 1000, 10*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 0, result.TransfersProcessed)
	_ = sessionCtx
}

func TestBackfillTask_CreatesSenderReceiverAndLinksLeafs(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	transferTime := time.Date(2025, time.December, 1, 12, 0, 0, 0, time.UTC)
	tr := createTransferWithLeafs(t, ctx, client, st.TransferStatusSenderKeyTweaked, transferTime, nil, 2)

	task := getBackfillTask(t)
	err := task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	senders, err := client.TransferSender.Query().
		Where(transfersender.TransferIDEQ(tr.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	assert.Equal(t, tr.SenderIdentityPubkey, senders[0].IdentityPubkey)
	assert.WithinDuration(t, transferTime, senders[0].CreateTime, time.Second)

	receivers, err := client.TransferReceiver.Query().
		Where(transferreceiver.TransferIDEQ(tr.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, receivers, 1)
	assert.Equal(t, tr.ReceiverIdentityPubkey, receivers[0].IdentityPubkey)
	assert.Equal(t, st.TransferReceiverStatusSenderInitiated, receivers[0].Status)
	assert.WithinDuration(t, transferTime, receivers[0].CreateTime, time.Second)

	leafs, err := client.TransferLeaf.Query().
		Where(transferleaf.TransferSenderIDNotNil()).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, leafs, 2)
	for _, leaf := range leafs {
		assert.Equal(t, senders[0].ID, *leaf.TransferSenderID)
		assert.Equal(t, receivers[0].ID, *leaf.TransferReceiverID)
	}
}

func TestBackfillTask_CompletedTransferWithCompletionTime(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	transferTime := time.Date(2025, time.November, 15, 8, 0, 0, 0, time.UTC)
	completionTime := time.Date(2025, time.November, 15, 8, 30, 0, 0, time.UTC)
	tr := createTransferWithLeafs(t, ctx, client, st.TransferStatusCompleted, transferTime, &completionTime, 1)

	task := getBackfillTask(t)
	err := task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	receivers, err := client.TransferReceiver.Query().
		Where(transferreceiver.TransferIDEQ(tr.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, receivers, 1)
	assert.Equal(t, st.TransferReceiverStatusCompleted, receivers[0].Status)
	assert.False(t, receivers[0].CompletionTime.IsZero())
	assert.WithinDuration(t, completionTime, receivers[0].CompletionTime, time.Second)
}

func TestBackfillTask_ExpiredTransferMapsToCancelled(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	tr := createTransferWithLeafs(t, ctx, client, st.TransferStatusExpired, time.Now(), nil, 1)

	task := getBackfillTask(t)
	err := task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	receivers, err := client.TransferReceiver.Query().
		Where(transferreceiver.TransferIDEQ(tr.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, receivers, 1)
	assert.Equal(t, st.TransferReceiverStatusCancelled, receivers[0].Status)
}

func TestBackfillTask_IdempotentOnSecondRun(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	createTransferWithLeafs(t, ctx, client, st.TransferStatusSenderInitiated, time.Now(), nil, 1)

	task := getBackfillTask(t)

	err := task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	senderCount, err := client.TransferSender.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, senderCount)

	err = task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	senderCount, err = client.TransferSender.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, senderCount)
}

func TestBackfillTask_SkipsUnspecifiedNetwork(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	senderKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()
	receiverKey := keys.MustGeneratePrivateKeyFromRand(rand.Reader).Public()

	client.Transfer.Create().
		SetNetwork(btcnetwork.Unspecified).
		SetStatus(st.TransferStatusReturned).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SaveX(ctx)

	task := getBackfillTask(t)
	err := task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	senderCount, err := client.TransferSender.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, senderCount)
}

func TestBackfillTask_ProcessesMultipleTransfers(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	baseTime := time.Date(2025, time.November, 1, 0, 0, 0, 0, time.UTC)
	createTransferWithLeafs(t, ctx, client, st.TransferStatusSenderInitiated, baseTime, nil, 1)
	createTransferWithLeafs(t, ctx, client, st.TransferStatusCompleted, baseTime.Add(1*time.Hour), nil, 1)
	createTransferWithLeafs(t, ctx, client, st.TransferStatusReceiverKeyTweaked, baseTime.Add(2*time.Hour), nil, 1)

	task := getBackfillTask(t)
	err := task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	senderCount, err := client.TransferSender.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, senderCount)

	receiverCount, err := client.TransferReceiver.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, receiverCount)
}

func TestBackfillTask_SkipsAlreadyBackfilledTransfers(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	oldTime := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)
	tr := createTransferWithLeafs(t, ctx, client, st.TransferStatusCompleted, oldTime, nil, 1)

	// Pre-create the sender and receiver (simulating already-backfilled).
	client.TransferSender.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(tr.SenderIdentityPubkey).
		SetCreateTime(oldTime).
		SaveX(ctx)
	client.TransferReceiver.Create().
		SetTransferID(tr.ID).
		SetIdentityPubkey(tr.ReceiverIdentityPubkey).
		SetStatus(st.TransferReceiverStatusCompleted).
		SetCreateTime(oldTime).
		SaveX(ctx)

	// Create a second transfer that needs backfilling.
	newTime := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	createTransferWithLeafs(t, ctx, client, st.TransferStatusSenderInitiated, newTime, nil, 1)

	task := getBackfillTask(t)
	err := task.RunOnce(ctx, cfg, client, nil, defaultKnobs())
	require.NoError(t, err)

	// Total senders should be 2 (one pre-existing + one backfilled).
	senderCount, err := client.TransferSender.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, senderCount)
}
