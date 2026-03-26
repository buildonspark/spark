package task

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func getCompleteInstantUtxoSwapTask() (ScheduledTaskSpec, error) {
	for _, t := range AllScheduledTasks() {
		if t.Name == "complete_instant_utxo_swap" {
			return t, nil
		}
	}
	return ScheduledTaskSpec{}, assert.AnError
}

func TestCompleteInstantUtxoSwap(t *testing.T) {
	tests := []struct {
		name           string
		transferStatus st.TransferStatus
		expectedStatus st.UtxoSwapStatus
	}{
		{
			name:           "transfer not sent - swap stays CREATED",
			transferStatus: st.TransferStatusSenderInitiated,
			expectedStatus: st.UtxoSwapStatusCreated,
		},
		{
			name:           "transfer sent - swap becomes COMPLETED",
			transferStatus: st.TransferStatusCompleted,
			expectedStatus: st.UtxoSwapStatusCompleted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, sessionCtx := db.ConnectToTestPostgres(t)
			client := sessionCtx.Client

			cfg := sparktesting.TestConfig(t)
			cfg.Index = 0
			pruneOperators(cfg)

			rng := rand.NewChaCha8([32]byte{})

			// BlockHeight for regtest — needed by VerifiedTargetUtxoFromRequest.
			_, err := client.BlockHeight.Create().
				SetNetwork(btcnetwork.Regtest).
				SetHeight(100).
				Save(ctx)
			require.NoError(t, err)

			// SigningKeyshare → DepositAddress → Utxo chain.
			secret := keys.MustGeneratePrivateKeyFromRand(rng)
			keyshare := client.SigningKeyshare.Create().
				SetStatus(st.KeyshareStatusAvailable).
				SetSecretShare(secret).
				SetPublicKey(secret.Public()).
				SetPublicShares(map[string]keys.Public{}).
				SetMinSigners(2).
				SetCoordinatorIndex(0).
				SaveX(ctx)

			ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			depositAddress := client.DepositAddress.Create().
				SetAddress("bc1ptest_instant_deposit").
				SetOwnerIdentityPubkey(ownerPubKey).
				SetOwnerSigningPubkey(ownerPubKey).
				SetSigningKeyshare(keyshare).
				SetIsStatic(true).
				SaveX(ctx)

			txid := make([]byte, 32)
			_, _ = rng.Read(txid)
			utxo := client.Utxo.Create().
				SetNetwork(btcnetwork.Regtest).
				SetTxid(txid).
				SetVout(0).
				SetBlockHeight(100).
				SetAmount(10000).
				SetPkScript([]byte("test_pk_script")).
				SetDepositAddress(depositAddress).
				SaveX(ctx)

			// Transfer with the test case's status.
			senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			transfer := client.Transfer.Create().
				SetNetwork(btcnetwork.Regtest).
				SetStatus(tc.transferStatus).
				SetType(st.TransferTypeTransfer).
				SetSenderIdentityPubkey(senderPubKey).
				SetReceiverIdentityPubkey(receiverPubKey).
				SetTotalValue(10000).
				SetExpiryTime(time.Now().Add(24 * time.Hour)).
				SaveX(ctx)

			// INSTANT utxo swap linked to utxo + transfer, old enough for the cron to pick up.
			utxoSwap := client.UtxoSwap.Create().
				SetStatus(st.UtxoSwapStatusCreated).
				SetRequestType(st.UtxoSwapRequestTypeInstant).
				SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
				SetUtxoValueSats(utxo.Amount).
				SetUtxo(utxo).
				SetTransfer(transfer).
				SetRequestedTransferID(transfer.ID).
				SetCreateTime(time.Now().Add(-5 * time.Minute)).
				SaveX(ctx)

			task, err := getCompleteInstantUtxoSwapTask()
			require.NoError(t, err)
			err = task.RunOnce(ctx, cfg, client, nil, knobs.NewFixedKnobs(map[string]float64{}))
			require.NoError(t, err)

			updated, err := client.UtxoSwap.Get(ctx, utxoSwap.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedStatus, updated.Status)
		})
	}
}
