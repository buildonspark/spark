package task

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCancelExpiredTransfers_CancelsSenderKeyTweakPendingUtxoSwap pins the
// recovery path for a stranded static-deposit claim: an aborted 2PC round can
// durably leave a UTXO_SWAP-typed secondary transfer at
// SENDER_KEY_TWEAK_PENDING with its leaves locked (the claim's rollback is a
// deliberate no-op), so the expiry sweep must return it and free the leaves —
// the same ownership it already has for PREIMAGE_SWAP rounds. An unexpired
// transfer in the same state must be left alone.
func TestCancelExpiredTransfers_CancelsSenderKeyTweakPendingUtxoSwap(t *testing.T) {
	t.Parallel()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	rng := rand.NewChaCha8([32]byte{72})
	senderIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createPreimageSwapExpirySigningKeyshare(t, ctx, rng, client)
	tree := createPreimageSwapExpiryTree(t, ctx, senderIdentityPubKey, client)
	leaf := createPreimageSwapExpiryLeaf(t, ctx, rng, client, tree, keyshare)

	expired, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(st.TransferTypeUtxoSwap).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(-1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	expiredReceiver, err := client.TransferReceiver.Create().
		SetTransferID(expired.ID).
		SetIdentityPubkey(receiverIdentityPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(expired.Type).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.TransferLeaf.Create().
		SetTransfer(expired).
		SetLeaf(leaf).
		SetTransferReceiver(expiredReceiver).
		SetPreviousRefundTx(preimageSwapExpiryRawTxBytes(t, 5)).
		SetIntermediateRefundTx(preimageSwapExpiryRawTxBytes(t, 6)).
		SetKeyTweak([]byte("sender-key-tweak")).
		Save(ctx)
	require.NoError(t, err)

	unexpired, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(st.TransferTypeUtxoSwap).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	cancelTask, err := findPreimageSwapExpiryCancelTask()
	require.NoError(t, err)
	require.NoError(t, cancelTask.RunOnce(ctx, cfg, client, nil, knobs.NewFixedKnobs(nil)))

	updatedExpired, err := client.Transfer.Get(ctx, expired.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReturned, updatedExpired.Status, "expired pending utxo-swap transfer must be returned")

	updatedLeaf, err := client.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, updatedLeaf.Status, "leaves must be unlocked")

	updatedUnexpired, err := client.Transfer.Get(ctx, unexpired.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, updatedUnexpired.Status, "unexpired transfer must be untouched")
}
