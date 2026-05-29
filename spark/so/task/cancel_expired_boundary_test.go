package task

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelExpiredTransfers_DoesNotCancelUnexpiredSenderInitiatedTransfers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		expiryTime time.Time
	}{
		{
			name:       "zero expiry",
			expiryTime: time.Unix(0, 0),
		},
		{
			name:       "future expiry",
			expiryTime: time.Now().Add(time.Hour),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, sessionCtx := db.ConnectToTestPostgres(t)
			client := sessionCtx.Client
			cfg := sparktesting.TestConfig(t)

			senderIdentityPubKey := keys.GeneratePrivateKey().Public()
			receiverIdentityPubKey := keys.GeneratePrivateKey().Public()
			transfer, receiver := createExpiryBoundaryTransferWithReceiver(
				ctx,
				t,
				client,
				senderIdentityPubKey,
				receiverIdentityPubKey,
				testCase.expiryTime,
			)

			cancelTask, err := findExpiryBoundaryCancelTask()
			require.NoError(t, err)

			err = cancelTask.RunOnce(ctx, cfg, client, nil, knobs.NewFixedKnobs(nil))
			require.NoError(t, err)

			updatedTransfer, err := client.Transfer.Get(ctx, transfer.ID)
			require.NoError(t, err)
			assert.Equal(t, st.TransferStatusSenderInitiated, updatedTransfer.Status)

			updatedReceiver, err := client.TransferReceiver.Get(ctx, receiver.ID)
			require.NoError(t, err)
			assert.Equal(t, st.TransferReceiverStatusInitiated, updatedReceiver.Status)
		})
	}
}

func createExpiryBoundaryTransferWithReceiver(
	ctx context.Context,
	t *testing.T,
	client *ent.Client,
	senderIdentityPubKey keys.Public,
	receiverIdentityPubKey keys.Public,
	expiryTime time.Time,
) (*ent.Transfer, *ent.TransferReceiver) {
	t.Helper()

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetTotalValue(1000).
		SetExpiryTime(expiryTime).
		Save(ctx)
	require.NoError(t, err)

	receiver, err := client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverIdentityPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	return transfer, receiver
}

func findExpiryBoundaryCancelTask() (ScheduledTaskSpec, error) {
	for _, scheduledTask := range AllScheduledTasks() {
		if scheduledTask.Name == "cancel_expired_transfers" {
			return scheduledTask, nil
		}
	}
	return ScheduledTaskSpec{}, fmt.Errorf("cancel_expired_transfers task not found in AllScheduledTasks")
}
