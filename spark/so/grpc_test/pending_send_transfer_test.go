package grpctest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// buildBadCoopExitRequest constructs a CooperativeExitRequest that targets the
// old code path (no TransferPackage) and is designed to fail AFTER the
// PendingSendTransfer record is committed. The failure point is the ExitTxid
// length check (expects 32 bytes, gets 31).
func buildBadCoopExitRequest(
	t *testing.T,
	config *wallet.TestWalletConfig,
	rootNode *sparkpb.TreeNode,
	leafPrivKey keys.Private,
) *sparkpb.CooperativeExitRequest {
	t.Helper()

	receiverKey := keys.GeneratePrivateKey()
	transferID, err := uuid.NewV7()
	require.NoError(t, err)
	exitID, err := uuid.NewV7()
	require.NoError(t, err)

	return &sparkpb.CooperativeExitRequest{
		Transfer: &sparkpb.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverKey.Public().Serialize(),
			LeavesToSend: []*sparkpb.LeafRefundTxSigningJob{{
				LeafId: rootNode.Id,
				RefundTxSigningJob: &sparkpb.SigningJob{
					RawTx:            rootNode.RefundTx,
					SigningPublicKey: leafPrivKey.Public().Serialize(),
				},
				DirectFromCpfpRefundTxSigningJob: &sparkpb.SigningJob{
					RawTx:            rootNode.RefundTx,
					SigningPublicKey: leafPrivKey.Public().Serialize(),
				},
			}},
			ExpiryTime: timestamppb.New(time.Now().Add(10 * time.Minute)),
		},
		ExitId:   exitID.String(),
		ExitTxid: make([]byte, 31), // deliberately too short → fails after createTransfer
	}
}

// TestCoopExitFailure_LeafReusableAfterBadExitTxid verifies that a failed
// cooperative exit properly cleans up so the sender's leaf remains usable.
//
// The failure is engineered via a 31-byte ExitTxid (must be 32). This fails
// AFTER PendingSendTransfer is committed and createTransfer succeeds,
// exercising the deferred rollback path in cooperativeExit.
func TestCoopExitFailure_LeafReusableAfterBadExitTxid(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100_000)
	require.NoError(t, err)

	conn, err := config.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)
	authCtx := wallet.ContextWithToken(t.Context(), token)

	client := sparkpb.NewSparkServiceClient(conn)

	req := buildBadCoopExitRequest(t, config, rootNode, leafPrivKey)
	_, err = client.CooperativeExitV2(authCtx, req)
	require.Error(t, err, "coop exit with 31-byte ExitTxid should fail")

	// The leaf should still be usable. A regular transfer proves the
	// rollback released leaf locks and cleaned up internal state.
	receiverKey := keys.GeneratePrivateKey()
	newLeafPrivKey := keys.GeneratePrivateKey()
	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(),
		config,
		[]wallet.LeafKeyTweak{transferNode},
		receiverKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "leaf should be usable after failed coop exit")
	require.NotNil(t, senderTransfer)
}

// TestCoopExitFailure_RetryWithSameTransferID verifies that after a failed
// cooperative exit, the same transfer ID can be reused without getting stuck
// on the PendingSendTransfer mutual exclusivity lock.
func TestCoopExitFailure_RetryWithSameTransferID(t *testing.T) {
	config := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(config, faucet, leafPrivKey, 100_000)
	require.NoError(t, err)

	conn, err := config.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), config, conn)
	require.NoError(t, err)

	// Use a bounded context so a lock regression fails fast instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	authCtx := wallet.ContextWithToken(ctx, token)

	client := sparkpb.NewSparkServiceClient(conn)

	req := buildBadCoopExitRequest(t, config, rootNode, leafPrivKey)

	// First attempt fails.
	_, err = client.CooperativeExitV2(authCtx, req)
	require.Error(t, err)

	// Second attempt with the same transfer ID should also fail (same bad
	// ExitTxid), but it must not hang on the PendingSendTransfer lock.
	_, err = client.CooperativeExitV2(authCtx, req)
	require.Error(t, err)
}
