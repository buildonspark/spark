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

func getCompleteUtxoSwapTask() (ScheduledTaskSpec, error) {
	for _, t := range AllScheduledTasks() {
		if t.Name == "complete_utxo_swap" {
			return t, nil
		}
	}
	return ScheduledTaskSpec{}, assert.AnError
}

// TestCompleteUtxoSwap_RefundLegacyStray pins the sweep's interaction with a
// stale CREATED refund swap. A CREATED refund row carrying this SO's key as
// coordinator can only be a legacy stray (consensus rows are never visible in
// that state — see the sweep's comment in task.go), and sweeping it keeps the
// legacy self-heal path alive: refund retries only re-sign against a COMPLETED
// swap, so an unswept stray would block the UTXO's active-swap slot
// indefinitely.
func TestCompleteUtxoSwap_RefundLegacyStray(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	cfg := sparktesting.TestConfig(t)
	cfg.Index = 0
	pruneOperators(cfg)

	rng := rand.NewChaCha8([32]byte{1})

	// BlockHeight for regtest — needed by VerifiedTargetUtxoFromRequest.
	_, err := client.BlockHeight.Create().
		SetNetwork(btcnetwork.Regtest).
		SetHeight(100).
		Save(ctx)
	require.NoError(t, err)

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
		SetAddress("bc1ptest_refund_deposit").
		SetOwnerIdentityPubkey(ownerPubKey).
		SetOwnerSigningPubkey(ownerPubKey).
		SetSigningKeyshare(keyshare).
		SetIsStatic(true).
		SaveX(ctx)

	txid := make([]byte, 32)
	_, _ = rng.Read(txid)
	// Well below the current block height (100) so the utxo passes the
	// sweep's default confirmation-threshold re-verification.
	utxo := client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txid).
		SetVout(0).
		SetBlockHeight(80).
		SetAmount(10000).
		SetPkScript([]byte("test_pk_script")).
		SetDepositAddress(depositAddress).
		SaveX(ctx)

	// REFUND swap in CREATED, coordinator = this SO, old enough for the sweep.
	utxoSwap := client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetUtxoValueSats(utxo.Amount).
		SetCreditAmountSats(utxo.Amount).
		SetSspIdentityPublicKey(ownerPubKey).
		SetUserIdentityPublicKey(ownerPubKey).
		SetUtxo(utxo).
		SetCreateTime(time.Now().Add(-10 * time.Minute)).
		SaveX(ctx)

	task, err := getCompleteUtxoSwapTask()
	require.NoError(t, err)
	err = task.RunOnce(ctx, cfg, client, nil, knobs.NewFixedKnobs(nil))
	require.NoError(t, err)

	updated, err := client.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updated.Status)
}
