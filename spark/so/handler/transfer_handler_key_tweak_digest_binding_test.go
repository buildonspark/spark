//go:build lightspark

package handler

import (
	"math/rand/v2"
	"testing"

	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// These tests pin the Commit-side digest binding: an SO must refuse to apply
// a stored receiver key tweak whose VSS proofs differ from the digest the
// coordinator committed. Applying it anyway silently tweaks this SO's
// keyshare with a different polynomial than its peers — the permanent
// divergence the prod incident produced. This contract is only observable at
// the SettleReceiverKeyTweak boundary (the gossip Commit path), which
// existing tests in transfer_handler_key_tweak_leaf_state_test.go already
// treat as the testing boundary.

func TestSettleReceiverKeyTweakCommitRejectsDigestMismatch(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{81})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
	originalOwnerSigningPubkey := leaf.OwnerSigningPubkey.Serialize()
	originalKeyTweak := append([]byte(nil), transferLeaf.KeyTweak...)

	bogusDigest := make([]byte, 32)
	for i := range bogusDigest {
		bogusDigest[i] = 0xEE
	}
	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
		LeafTweakDigests: []*pbinternal.ClaimLeafTweakDigest{
			{LeafId: leaf.ID.String(), ProofsHash: bogusDigest},
		},
	})
	require.Error(t, err, "a stored tweak from a different polynomial than the committed digest must not be applied")
	require.ErrorContains(t, err, "tweak digest")

	// Commit the handler's transaction before asserting: uncommitted writes
	// are invisible to sessionCtx.Client reads, so without this the
	// "nothing applied" checks below would pass even if the digest check
	// fired only after a mutation. Committing exposes any such write.
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	// Nothing may have been applied: owner unchanged, stored tweak intact,
	// transfer still pre-apply.
	updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	require.Equal(t, originalOwnerSigningPubkey, updatedLeaf.OwnerSigningPubkey.Serialize())
	updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	require.Equal(t, originalKeyTweak, updatedTransferLeaf.KeyTweak)
	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReceiverKeyTweakLocked, updatedTransfer.Status)
}

func TestSettleReceiverKeyTweakCommitAppliesWithMatchingDigest(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{82})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
	stored := &pb.ClaimLeafKeyTweak{}
	require.NoError(t, proto.Unmarshal(transferLeaf.KeyTweak, stored))

	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
		LeafTweakDigests: []*pbinternal.ClaimLeafTweakDigest{
			{LeafId: leaf.ID.String(), ProofsHash: claimTweakProofsDigest(stored.GetSecretShareTweak().GetProofs())},
		},
	})
	require.NoError(t, err, "a matching digest must apply exactly like a digest-free commit")

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
}

func TestSettleReceiverKeyTweakCommitRejectsDigestMissingForLeaf(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{83})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	leaf, transfer, _ := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)

	// Digest list present but covers a different leaf — the request claims to
	// bind digests yet leaves this leaf unbound; refusing is the safe reading.
	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
		LeafTweakDigests: []*pbinternal.ClaimLeafTweakDigest{
			{LeafId: "99999999-9999-9999-9999-999999999999", ProofsHash: make([]byte, 32)},
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "tweak digest")

	// Commit before asserting so a mutation-before-check regression would be
	// visible to the separate sessionCtx.Client connection.
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReceiverKeyTweakLocked, updatedTransfer.Status)
	_ = leaf
}
