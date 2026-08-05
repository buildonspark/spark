//go:build lightspark

package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type scopedReceiverArm struct {
	receiver     *ent.TransferReceiver
	leaf         *ent.TreeNode
	transferLeaf *ent.TransferLeaf
}

// createTwoReceiverClaimFixture builds one transfer with two independent
// receivers, each owning its own leaf with a stored key tweak, both parked at
// KEY_TWEAK_LOCKED. This is the shape that makes receiver-scoped leaf queries
// load-bearing: an unscoped query returns both arms' leaves.
func createTwoReceiverClaimFixture(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	rng *rand.ChaCha8,
	transferStatus st.TransferStatus,
	receiverStatus st.TransferReceiverStatus,
) (*ent.Transfer, scopedReceiverArm, scopedReceiverArm) {
	t.Helper()

	receiverAPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverBPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(transferStatus).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPubKey).
		SetReceiverIdentityPubkey(receiverAPubKey).
		SetTotalValue(2000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), client)

	buildArm := func(receiverPubKey keys.Public) scopedReceiverArm {
		// A keyshare per leaf: the duplicate-keyshare guard rejects a settle
		// whose leaves share one.
		keyshare := createTestSigningKeyshare(t, ctx, rng, client)
		leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)
		receiver, err := client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(receiverStatus).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		transferLeaf := createTestTransferLeaf(t, ctx, client, transfer, leaf)
		transferLeaf, err = transferLeaf.Update().
			SetTransferReceiverID(receiver.ID).
			SetKeyTweak(createReceiverClaimKeyTweakBytes(t, cfg, rng, leaf.ID)).
			Save(ctx)
		require.NoError(t, err)

		return scopedReceiverArm{receiver: receiver, leaf: leaf, transferLeaf: transferLeaf}
	}

	return transfer, buildArm(receiverAPubKey), buildArm(receiverBPubKey)
}

// TestSettleReceiverKeyTweakScopesToRequestedReceiver pins the invariant that
// keeps one receiver's claim from touching another's funds: settling receiver A
// applies A's key tweak to A's leaf only, leaving B's leaf ownership, B's stored
// tweak, and B's receiver row untouched.
func TestSettleReceiverKeyTweakScopesToRequestedReceiver(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{91})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	transfer, armA, armB := createTwoReceiverClaimFixture(
		t, ctx, sessionCtx.Client, cfg, rng,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferReceiverStatusKeyTweakLocked,
	)
	originalBOwnerSigningPubkey := armB.leaf.OwnerSigningPubkey.Serialize()
	originalBOwnerIdentityPubkey := armB.leaf.OwnerIdentityPubkey.Serialize()
	originalBKeyTweak := append([]byte(nil), armB.transferLeaf.KeyTweak...)

	require.NoError(t, handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId:                transfer.ID.String(),
		Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
		ReceiverIdentityPublicKey: armA.receiver.IdentityPubkey.Serialize(),
	}))

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	settledA, err := sessionCtx.Client.TreeNode.Get(ctx, armA.leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, armA.receiver.IdentityPubkey.Serialize(), settledA.OwnerIdentityPubkey.Serialize(),
		"receiver A's leaf should have moved to receiver A")
	assert.NotEqual(t, armA.leaf.OwnerSigningPubkey.Serialize(), settledA.OwnerSigningPubkey.Serialize())
	assert.True(t, settledA.UpdateTime.After(armA.leaf.UpdateTime),
		"settled leaf's update_time must advance so incremental consumers cursoring on it see the owner-key change")

	settledATransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, armA.transferLeaf.ID)
	require.NoError(t, err)
	assert.Empty(t, settledATransferLeaf.KeyTweak, "receiver A's stored tweak should be consumed")

	untouchedB, err := sessionCtx.Client.TreeNode.Get(ctx, armB.leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, originalBOwnerIdentityPubkey, untouchedB.OwnerIdentityPubkey.Serialize(),
		"receiver B's leaf must not change owner when receiver A settles")
	assert.Equal(t, originalBOwnerSigningPubkey, untouchedB.OwnerSigningPubkey.Serialize(),
		"receiver B's leaf signing key must not be rewritten by receiver A's settle")

	untouchedBTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, armB.transferLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, originalBKeyTweak, untouchedBTransferLeaf.KeyTweak,
		"receiver B's stored tweak must survive receiver A's settle")

	receiverA, err := sessionCtx.Client.TransferReceiver.Get(ctx, armA.receiver.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferReceiverStatusKeyTweakApplied, receiverA.Status)

	receiverB, err := sessionCtx.Client.TransferReceiver.Get(ctx, armB.receiver.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferReceiverStatusKeyTweakLocked, receiverB.Status,
		"receiver B must stay at KEY_TWEAK_LOCKED until it settles itself")
}

// TestSettleReceiverKeyTweakOmittedReceiverPubkeyUsesPrimary pins the fallback
// when the internal request omits receiver_identity_public_key: the transfer's
// own receiver identity is used, and only that arm's row and leaves settle.
func TestSettleReceiverKeyTweakOmittedReceiverPubkeyUsesPrimary(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{92})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	transfer, armA, armB := createTwoReceiverClaimFixture(
		t, ctx, sessionCtx.Client, cfg, rng,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferReceiverStatusKeyTweakLocked,
	)
	require.Equal(t, transfer.ReceiverIdentityPubkey, armA.receiver.IdentityPubkey,
		"fixture invariant: arm A is the transfer's primary receiver")
	originalBOwnerIdentityPubkey := armB.leaf.OwnerIdentityPubkey.Serialize()
	originalBOwnerSigningPubkey := armB.leaf.OwnerSigningPubkey.Serialize()
	originalBKeyTweak := append([]byte(nil), armB.transferLeaf.KeyTweak...)

	require.NoError(t, handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	}))

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	receiverA, err := sessionCtx.Client.TransferReceiver.Get(ctx, armA.receiver.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferReceiverStatusKeyTweakApplied, receiverA.Status)

	settledA, err := sessionCtx.Client.TreeNode.Get(ctx, armA.leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, armA.receiver.IdentityPubkey.Serialize(), settledA.OwnerIdentityPubkey.Serialize())

	receiverB, err := sessionCtx.Client.TransferReceiver.Get(ctx, armB.receiver.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferReceiverStatusKeyTweakLocked, receiverB.Status)

	untouchedB, err := sessionCtx.Client.TreeNode.Get(ctx, armB.leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, originalBOwnerIdentityPubkey, untouchedB.OwnerIdentityPubkey.Serialize(),
		"the fallback must not settle receiver B's leaf")
	assert.Equal(t, originalBOwnerSigningPubkey, untouchedB.OwnerSigningPubkey.Serialize())

	untouchedBTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, armB.transferLeaf.ID)
	require.NoError(t, err)
	assert.Equal(t, originalBKeyTweak, untouchedBTransferLeaf.KeyTweak)
}

// TestSettleReceiverKeyTweakRejectsTransferWithoutReceiverRows pins the
// endpoint-level rejection when a transfer carries no receiver rows at all —
// the one input for which the receiver load yields nothing.
func TestSettleReceiverKeyTweakRejectsTransferWithoutReceiverRows(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{93})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	transfer := createTestTransfer(t, ctx, rng, sessionCtx.Client, st.TransferStatusReceiverKeyTweakLocked)

	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no transfer receivers found for transfer")
}

// TestClaimTransferCommitReceiverStatusGates covers the 2PC Commit entry gates,
// which branch on the claimer's receiver status: an already-completed receiver
// is an idempotent replay of the commit gossip, and a receiver that never
// reached Phase 1 must be refused rather than applied.
func TestClaimTransferCommitReceiverStatusGates(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{94})
	cfg := sparktesting.TestConfig(t)

	t.Run("completed receiver is an idempotent no-op", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		transfer, armA, _ := createTwoReceiverClaimFixture(
			t, ctx, sessionCtx.Client, cfg, rng,
			st.TransferStatusReceiverKeyTweakLocked,
			st.TransferReceiverStatusCompleted,
		)

		require.NoError(t, NewClaimTransferFlowHandler(cfg).Commit(ctx, &pbinternal.ClaimTransferCommitRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: armA.receiver.IdentityPubkey.Serialize(),
		}))
	})

	t.Run("receiver that never locked is refused", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		transfer, armA, _ := createTwoReceiverClaimFixture(
			t, ctx, sessionCtx.Client, cfg, rng,
			st.TransferStatusSenderKeyTweaked,
			st.TransferReceiverStatusReceiverClaimPending,
		)

		err := NewClaimTransferFlowHandler(cfg).Commit(ctx, &pbinternal.ClaimTransferCommitRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: armA.receiver.IdentityPubkey.Serialize(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status")
	})
}

// TestClaimTransferRollbackReceiverScoped covers the 2PC Rollback body: it must
// revert only the requested receiver, no-op on a transfer it cannot find, and
// report an already-committed claim as AlreadyExists — the gossip rollback
// dispatcher treats only that code as success, so a bare error would leave the
// flow row IN_FLIGHT and the reconciler retrying forever.
func TestClaimTransferRollbackReceiverScoped(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{95})
	cfg := sparktesting.TestConfig(t)

	t.Run("unknown transfer is a no-op", func(t *testing.T) {
		ctx, _ := db.ConnectToTestPostgres(t)
		require.NoError(t, NewClaimTransferFlowHandler(cfg).Rollback(ctx, &pbinternal.ClaimTransferRollbackRequest{
			TransferId: uuid.New().String(),
		}))
	})

	t.Run("locked receiver reverts, sibling untouched", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		transfer, armA, armB := createTwoReceiverClaimFixture(
			t, ctx, sessionCtx.Client, cfg, rng,
			st.TransferStatusReceiverKeyTweakLocked,
			st.TransferReceiverStatusKeyTweakLocked,
		)
		originalBKeyTweak := append([]byte(nil), armB.transferLeaf.KeyTweak...)

		require.NoError(t, NewClaimTransferFlowHandler(cfg).Rollback(ctx, &pbinternal.ClaimTransferRollbackRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: armA.receiver.IdentityPubkey.Serialize(),
		}))

		entTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)
		require.NoError(t, entTx.Commit())

		revertedA, err := sessionCtx.Client.TransferReceiver.Get(ctx, armA.receiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusReceiverClaimPending, revertedA.Status)

		revertedALeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, armA.transferLeaf.ID)
		require.NoError(t, err)
		assert.Empty(t, revertedALeaf.KeyTweak, "rollback must clear the reverted receiver's stored tweak")

		untouchedB, err := sessionCtx.Client.TransferLeaf.Get(ctx, armB.transferLeaf.ID)
		require.NoError(t, err)
		assert.Equal(t, originalBKeyTweak, untouchedB.KeyTweak,
			"rollback of receiver A must not clear receiver B's stored tweak")

		reloadedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, reloadedTransfer.Status)
	})

	t.Run("already-applied claim reports AlreadyExists", func(t *testing.T) {
		ctx, sessionCtx := db.ConnectToTestPostgres(t)
		transfer, armA, _ := createTwoReceiverClaimFixture(
			t, ctx, sessionCtx.Client, cfg, rng,
			st.TransferStatusReceiverKeyTweakApplied,
			st.TransferReceiverStatusKeyTweakApplied,
		)

		err := NewClaimTransferFlowHandler(cfg).Rollback(ctx, &pbinternal.ClaimTransferRollbackRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: armA.receiver.IdentityPubkey.Serialize(),
		})
		require.Error(t, err)
		assert.Equal(t, codes.AlreadyExists, status.Code(err),
			"the gossip rollback dispatcher only treats AlreadyExists as success")
	})
}
