//go:build lightspark

package handler

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCommitSenderKeyTweaks_RejectsExitedLeaves is a regression test for spark#5946.
// It verifies that commitSenderKeyTweaks (in BaseTransferHandler) rejects the
// transfer when any leaf has been exited to L1. This is the unified guard that
// applies to all transfer types, preventing double-spend via concurrent L1 exit.
func TestCommitSenderKeyTweaks_RejectsExitedLeaves(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{59})

	client := dbCtx.Client

	for _, tc := range []struct {
		name           string
		leafStatus     st.TreeNodeStatus
		expectedReject bool
	}{
		{"exited leaf rejects key tweak commit", st.TreeNodeStatusExited, true},
		{"on-chain leaf rejects key tweak commit", st.TreeNodeStatusOnChain, true},
		{"parent-exited leaf rejects key tweak commit", st.TreeNodeStatusParentExited, true},
		{"available leaf proceeds to key tweak commit", st.TreeNodeStatusAvailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

			keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			signingKeyshare, err := client.SigningKeyshare.Create().
				SetStatus(st.KeyshareStatusInUse).
				SetSecretShare(keysharePrivKey).
				SetPublicShares(map[string]keys.Public{"op1": publicSharePrivKey.Public()}).
				SetPublicKey(keysharePrivKey.Public()).
				SetMinSigners(2).
				SetCoordinatorIndex(0).
				Save(ctx)
			require.NoError(t, err)

			tree, err := client.Tree.Create().
				SetStatus(st.TreeStatusAvailable).
				SetNetwork(btcnetwork.Regtest).
				SetOwnerIdentityPubkey(senderPrivKey.Public()).
				SetBaseTxid(st.NewRandomTxIDForTesting(t)).
				SetVout(0).
				Save(ctx)
			require.NoError(t, err)

			verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			leaf, err := client.TreeNode.Create().
				SetStatus(tc.leafStatus).
				SetTree(tree).
				SetNetwork(tree.Network).
				SetSigningKeyshare(signingKeyshare).
				SetValue(1000).
				SetVerifyingPubkey(verifyingPrivKey.Public()).
				SetOwnerIdentityPubkey(senderPrivKey.Public()).
				SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
				SetRawTx(createTestTxBytes(t, 3000)).
				SetRawRefundTx(createTestTxBytes(t, 3100)).
				SetVout(0).
				Save(ctx)
			require.NoError(t, err)

			transfer, err := client.Transfer.Create().
				SetNetwork(btcnetwork.Regtest).
				SetStatus(st.TransferStatusSenderKeyTweakPending).
				SetType(st.TransferTypePreimageSwap).
				SetSenderIdentityPubkey(senderPrivKey.Public()).
				SetReceiverIdentityPubkey(receiverPrivKey.Public()).
				SetTotalValue(1000).
				SetExpiryTime(time.Now().Add(24 * time.Hour)).
				Save(ctx)
			require.NoError(t, err)

			_, err = client.TransferLeaf.Create().
				SetTransfer(transfer).
				SetLeaf(leaf).
				SetPreviousRefundTx(createTestTxBytes(t, 4000)).
				SetIntermediateRefundTx(createTestTxBytes(t, 4001)).
				Save(ctx)
			require.NoError(t, err)

			// Exercise the unified guard via SettleSenderKeyTweak, which calls
			// commitSenderKeyTweaks in BaseTransferHandler.
			handler := NewInternalTransferHandler(cfg)
			err = handler.SettleSenderKeyTweak(ctx, &pbinternal.SettleSenderKeyTweakRequest{
				TransferId: transfer.ID.String(),
				Action:     pbinternal.SettleKeyTweakAction_COMMIT,
			})

			if tc.expectedReject {
				require.Error(t, err)
				grpcStatus, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error, got: %v", err)
				require.Equal(t, codes.FailedPrecondition, grpcStatus.Code())
				assert.Contains(t, err.Error(), "exited to L1")
			} else {
				// For available leaves, the error should NOT be about L1 exit.
				// It will fail later (e.g., no key tweak stored), which is expected —
				// the important thing is we pass the L1 exit check.
				if err != nil {
					assert.NotContains(t, err.Error(), "exited to L1")
				}
			}
		})
	}
}
