package grpctest

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

// TestClaimTransferWithExitedToL1Leaf verifies that a receiver can still claim
// a pending transfer after one of its leaves exits to L1 mid-transfer (e.g. a
// malicious sender starts a unilateral exit after handing over the key tweak).
// The claim must succeed so the watchtower can broadcast the newest refund tx
// for the receiver, but the leaf must keep its on-chain status — it must not
// be revived to AVAILABLE, and it must not be transferable afterwards.
func TestClaimTransferWithExitedToL1Leaf(t *testing.T) {
	for _, leafStatus := range []st.TreeNodeStatus{
		st.TreeNodeStatusOnChain,
		st.TreeNodeStatusExited,
		st.TreeNodeStatusParentExited,
	} {
		t.Run(string(leafStatus), func(t *testing.T) {
			senderConfig := wallet.NewTestWalletConfig(t)
			leafPrivKey := keys.GeneratePrivateKey()
			rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
			require.NoError(t, err, "failed to create new tree")

			newLeafPrivKey := keys.GeneratePrivateKey()
			receiverPrivKey := keys.GeneratePrivateKey()

			transferNode := wallet.LeafKeyTweak{
				Leaf:              rootNode,
				SigningPrivKey:    leafPrivKey,
				NewSigningPrivKey: newLeafPrivKey,
			}
			senderTransfer, err := wallet.SendTransferWithKeyTweaks(
				t.Context(),
				senderConfig,
				[]wallet.LeafKeyTweak{transferNode},
				receiverPrivKey.Public(),
				time.Now().Add(10*time.Minute),
			)
			require.NoError(t, err, "failed to send transfer")

			// Simulate the chain watcher (tree.MarkExitingNodes) observing the
			// sender's unilateral-exit txs confirm while the transfer is pending:
			// the node tx confirming marks the leaf ON_CHAIN, the refund tx
			// confirming marks it EXITED.
			leafID, err := uuid.Parse(rootNode.GetId())
			require.NoError(t, err)
			operatorIndices := operatorIndicesFromConfig(senderConfig)
			for _, i := range operatorIndices {
				client := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
				_, err := client.TreeNode.UpdateOneID(leafID).SetStatus(leafStatus).Save(t.Context())
				require.NoError(t, err, "failed to mark leaf %s on operator %d", leafStatus, i)
				require.NoError(t, client.Close())
			}

			receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)
			receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
			require.NoError(t, err, "failed to authenticate receiver")
			receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)
			pendingTransfer, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
			require.NoError(t, err, "failed to query pending transfers")
			require.Len(t, pendingTransfer.GetTransfers(), 1)
			receiverTransfer := pendingTransfer.GetTransfers()[0]
			require.Equal(t, senderTransfer.GetId(), receiverTransfer.GetId())

			_, err = wallet.VerifyPendingTransfer(t.Context(), receiverConfig, receiverTransfer)
			require.NoError(t, err)

			finalLeafPrivKey := keys.GeneratePrivateKey()
			claimingNode := wallet.LeafKeyTweak{
				Leaf:              receiverTransfer.GetLeaves()[0].GetLeaf(),
				SigningPrivKey:    newLeafPrivKey,
				NewSigningPrivKey: finalLeafPrivKey,
			}
			claimedTransfer, err := wallet.ClaimTransferV2(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimingNode})
			require.NoError(t, err, "claim must succeed for a leaf with status %s", leafStatus)
			require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_COMPLETED, claimedTransfer.GetStatus())

			// Every operator must have transferred ownership to the receiver
			// while preserving the leaf's on-chain status.
			for _, i := range operatorIndices {
				client := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, i))
				leaf, err := client.TreeNode.Get(t.Context(), leafID)
				require.NoError(t, err)
				require.Equal(t, leafStatus, leaf.Status, "leaf status must be preserved on operator %d", i)
				require.True(t, leaf.OwnerIdentityPubkey.Equals(receiverPrivKey.Public()),
					"leaf owner must be the receiver on operator %d", i)
				require.NoError(t, client.Close())
			}

			// The claimed leaf exited to L1, so it must not be transferable again.
			onwardPrivKey := keys.GeneratePrivateKey()
			_, err = wallet.SendTransferWithKeyTweaks(
				receiverCtx,
				receiverConfig,
				[]wallet.LeafKeyTweak{{
					Leaf:              claimedTransfer.GetLeaves()[0].GetLeaf(),
					SigningPrivKey:    finalLeafPrivKey,
					NewSigningPrivKey: onwardPrivKey,
				}},
				keys.GeneratePrivateKey().Public(),
				time.Now().Add(10*time.Minute),
			)
			require.Error(t, err, "an exited leaf must not be transferable after claim")
		})
	}
}
