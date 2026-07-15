//go:build lightspark

package handler

import (
	"context"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSettleReceiverKeyTweakRejectsNonTransferLockedLeaf(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{71})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	nonTransferLockedStatuses := []st.TreeNodeStatus{
		st.TreeNodeStatusCreating,
		st.TreeNodeStatusAvailable,
		st.TreeNodeStatusFrozenByIssuer,
		st.TreeNodeStatusSplitLocked,
		st.TreeNodeStatusSplitted,
		st.TreeNodeStatusAggregated,
		st.TreeNodeStatusAggregateLock,
		st.TreeNodeStatusInvestigation,
		st.TreeNodeStatusLost,
		st.TreeNodeStatusReimbursed,
		st.TreeNodeStatusRenewLocked,
	}

	for _, leafStatus := range nonTransferLockedStatuses {
		t.Run(string(leafStatus), func(t *testing.T) {
			leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
			leaf, err := leaf.Update().SetStatus(leafStatus).Save(ctx)
			require.NoError(t, err)

			originalOwnerIdentityPubkey := leaf.OwnerIdentityPubkey.Serialize()
			originalOwnerSigningPubkey := leaf.OwnerSigningPubkey.Serialize()
			originalKeyTweak := append([]byte(nil), transferLeaf.KeyTweak...)

			err = handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
				TransferId: transfer.ID.String(),
				Action:     pbinternal.SettleKeyTweakAction_COMMIT,
			})
			require.Error(t, err)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.ErrorContains(t, err, "must be TRANSFER_LOCKED or exited to L1 to claim receiver key tweak")

			updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			require.Equal(t, leafStatus, updatedLeaf.Status)
			require.Equal(t, originalOwnerIdentityPubkey, updatedLeaf.OwnerIdentityPubkey.Serialize())
			require.Equal(t, originalOwnerSigningPubkey, updatedLeaf.OwnerSigningPubkey.Serialize())

			updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
			require.NoError(t, err)
			require.Equal(t, originalKeyTweak, updatedTransferLeaf.KeyTweak)

			updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
			require.NoError(t, err)
			require.Equal(t, st.TransferStatusReceiverKeyTweakLocked, updatedTransfer.Status)
		})
	}
}

func TestSettleReceiverKeyTweakAppliesExitedToL1LeafPreservingStatus(t *testing.T) {
	exitedStatuses := []st.TreeNodeStatus{
		st.TreeNodeStatusOnChain,
		st.TreeNodeStatusExited,
		st.TreeNodeStatusParentExited,
	}

	for i, leafStatus := range exitedStatuses {
		t.Run(string(leafStatus), func(t *testing.T) {
			ctx, sessionCtx := db.ConnectToTestPostgres(t)
			rng := rand.NewChaCha8([32]byte{byte(73 + i)})
			cfg := sparktesting.TestConfig(t)
			handler := NewTransferHandler(cfg)

			leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
			leaf, err := leaf.Update().SetStatus(leafStatus).Save(ctx)
			require.NoError(t, err)
			originalOwnerSigningPubkey := leaf.OwnerSigningPubkey.Serialize()

			err = handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
				TransferId: transfer.ID.String(),
				Action:     pbinternal.SettleKeyTweakAction_COMMIT,
			})
			require.NoError(t, err)

			entTx, err := ent.GetTxFromContext(ctx)
			require.NoError(t, err)
			require.NoError(t, entTx.Commit())

			// Ownership moves to the receiver, but the on-chain status is preserved.
			updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			require.Equal(t, leafStatus, updatedLeaf.Status)
			require.Equal(t, transfer.ReceiverIdentityPubkey.Serialize(), updatedLeaf.OwnerIdentityPubkey.Serialize())
			require.NotEqual(t, originalOwnerSigningPubkey, updatedLeaf.OwnerSigningPubkey.Serialize())

			updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
			require.NoError(t, err)
			require.Empty(t, updatedTransferLeaf.KeyTweak)

			updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
			require.NoError(t, err)
			require.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
		})
	}
}

func TestSettleReceiverKeyTweakAppliesTransferLockedLeaf(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{72})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	leaf, transfer, transferLeaf := createReceiverKeyTweakSettlementFixture(t, ctx, sessionCtx.Client, cfg, rng)
	originalOwnerSigningPubkey := leaf.OwnerSigningPubkey.Serialize()

	err := handler.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.NoError(t, err)

	// SettleReceiverKeyTweak intentionally does not commit the surrounding
	// ent transaction (the caller owns the commit lifecycle), so commit here
	// before asserting through the plain client.
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	updatedLeaf, err := sessionCtx.Client.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, updatedLeaf.Status)
	require.Equal(t, transfer.ReceiverIdentityPubkey.Serialize(), updatedLeaf.OwnerIdentityPubkey.Serialize())
	require.NotEqual(t, originalOwnerSigningPubkey, updatedLeaf.OwnerSigningPubkey.Serialize())

	updatedTransferLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	require.Empty(t, updatedTransferLeaf.KeyTweak)

	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
}

func createReceiverKeyTweakSettlementFixture(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	rng *rand.ChaCha8,
) (*ent.TreeNode, *ent.Transfer, *ent.TransferLeaf) {
	t.Helper()

	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), client)
	leaf := createTestTreeNode(t, ctx, rng, client, tree, keyshare)
	transfer := createTestTransfer(t, ctx, rng, client, st.TransferStatusReceiverKeyTweakLocked)
	transferLeaf := createTestTransferLeaf(t, ctx, client, transfer, leaf)

	keyTweakBytes := createReceiverClaimKeyTweakBytes(t, cfg, rng, leaf.ID)
	transferLeaf, err := transferLeaf.Update().SetKeyTweak(keyTweakBytes).Save(ctx)
	require.NoError(t, err)

	return leaf, transfer, transferLeaf
}

func createReceiverClaimKeyTweakBytes(t *testing.T, cfg *so.Config, rng *rand.ChaCha8, leafID uuid.UUID) []byte {
	t.Helper()

	tweakPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(tweakPrivKey.Serialize())
	shares, err := secretsharing.SplitSecretWithProofs(
		secretInt,
		secp256k1.S256().N,
		int(cfg.Threshold),
		len(cfg.SigningOperatorMap),
	)
	require.NoError(t, err)
	require.NotEmpty(t, shares)

	secretShareBytes := make([]byte, 32)
	shares[0].Share.FillBytes(secretShareBytes)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		LeafId: leafID.String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      shares[0].Proofs,
		},
		PubkeySharesTweak: buildValidPubkeySharesTweak(t, cfg, shares[0].Proofs),
	}
	keyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)
	return keyTweakBytes
}
