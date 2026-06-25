//go:build lightspark

package handler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func TestUpdateTransferRejectsTerminalSyncUnlockForNonTransferLockedLeaf(t *testing.T) {
	statuses := []st.TreeNodeStatus{
		st.TreeNodeStatusCreating,
		st.TreeNodeStatusFrozenByIssuer,
		st.TreeNodeStatusSplitLocked,
		st.TreeNodeStatusAggregated,
		st.TreeNodeStatusAggregateLock,
		st.TreeNodeStatusInvestigation,
		st.TreeNodeStatusLost,
		st.TreeNodeStatusRenewLocked,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			client, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			sender := keys.GeneratePrivateKey().Public()
			receiver := keys.GeneratePrivateKey().Public()
			leaf := createTerminalSyncTestLeaf(t, ctx, client, sender, status)
			transfer := createTerminalSyncTestTransfer(t, ctx, client, leaf, sender, receiver)

			err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
				ctx,
				&pb.Transfer{
					Id:     transfer.ID.String(),
					Status: pb.TransferStatus_TRANSFER_STATUS_RETURNED,
					Type:   pb.TransferType_TRANSFER,
				},
				&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
			)
			require.Error(t, err)
			require.ErrorContains(t, err, "failed to validate terminal sync unlock for leaf")
			require.ErrorContains(t, err, "cannot be unlocked by terminal sync")

			refreshed, err := client.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			require.Equal(t, status, refreshed.Status)
		})
	}
}

func TestValidateSyncTransferTerminalLeafCanUnlockAllowsCompletableStatuses(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus st.TreeNodeStatus
	}{
		{
			name:          "transfer locked unlocks",
			initialStatus: st.TreeNodeStatusTransferLocked,
		},
		{
			name:          "available remains available",
			initialStatus: st.TreeNodeStatusAvailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			client, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			sender := keys.GeneratePrivateKey().Public()
			receiver := keys.GeneratePrivateKey().Public()
			leaf := createTerminalSyncTestLeaf(t, ctx, client, sender, tt.initialStatus)
			transfer := createTerminalSyncTestTransfer(t, ctx, client, leaf, sender, receiver)

			err = validateSyncTransferTerminalLeafCanUnlock(leaf, transfer)
			require.NoError(t, err)

			_, err = leaf.Update().SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
			require.NoError(t, err)

			refreshed, err := client.TreeNode.Get(ctx, leaf.ID)
			require.NoError(t, err)
			require.Equal(t, st.TreeNodeStatusAvailable, refreshed.Status)
		})
	}
}

func createTerminalSyncTestLeaf(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	owner keys.Public,
	status st.TreeNodeStatus,
) *ent.TreeNode {
	t.Helper()

	tree, err := client.Tree.Create().
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(owner).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetStatus(st.TreeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	secret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetPublicShares(map[string]keys.Public{"key": secret.Public()}).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicKey(secret.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	leaf, err := client.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetStatus(status).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(owner).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetRawTx(createTestTxBytesWithIndex(t, 1000, 0)).
		SetRawRefundTx(createTestTxBytes(t, 900)).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)
	return leaf
}

func createTerminalSyncTestTransfer(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	leaf *ent.TreeNode,
	sender keys.Public,
	receiver keys.Public,
) *ent.Transfer {
	t.Helper()

	transfer, err := client.Transfer.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetType(st.TransferTypeTransfer).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetTotalValue(leaf.Value).
		SetExpiryTime(time.Now().Add(time.Hour)).
		SetSenderIdentityPubkey(sender).
		SetReceiverIdentityPubkey(receiver).
		Save(ctx)
	require.NoError(t, err)

	transferSender, err := createTransferSender(ctx, client, transfer, sender)
	require.NoError(t, err)
	transferReceiver, err := createTransferReceiver(ctx, client, transfer, receiver, st.TransferReceiverStatusReceiverClaimPending)
	require.NoError(t, err)

	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetTransferSender(transferSender).
		SetTransferReceiver(transferReceiver).
		SetPreviousRefundTx(leaf.RawRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 800)).
		SetSecretCipher([]byte("secret")).
		SetSignature([]byte("signature")).
		Save(ctx)
	require.NoError(t, err)

	return transfer
}
