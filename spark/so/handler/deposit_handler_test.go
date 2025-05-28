package handler

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/enttest"
	"github.com/lightsparkdev/spark/so/ent/schema"
	testutil "github.com/lightsparkdev/spark/test_util"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifiedTargetUtxo(t *testing.T) {
	ctx := context.Background()

	dbClient := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	tx, err := dbClient.Tx(ctx)
	require.NoError(t, err)

	ctx = context.WithValue(ctx, ent.TxKey, tx)
	defer dbClient.Close()

	// Create test data
	blockHeight := 100
	txid := []byte("test_txid")
	vout := uint32(0)

	// Create block height records for both networks
	_, err = tx.BlockHeight.Create().
		SetNetwork(schema.NetworkMainnet).
		SetHeight(int64(blockHeight)).
		Save(ctx)
	testutil.OnErrFatal(t, err)

	_, err = tx.BlockHeight.Create().
		SetNetwork(schema.NetworkRegtest).
		SetHeight(int64(blockHeight)).
		Save(ctx)
	testutil.OnErrFatal(t, err)

	t.Run("successful verification", func(t *testing.T) {
		// Create signing keyshare first
		signingKeyshare, err := tx.SigningKeyshare.Create().
			SetStatus(schema.KeyshareStatusAvailable).
			SetSecretShare([]byte("test_secret_share")).
			SetPublicShares(map[string][]byte{"test": []byte("test_public_share")}).
			SetPublicKey([]byte("test_public_key")).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		// Create deposit address
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress("test_address").
			SetOwnerIdentityPubkey([]byte("test_identity_pubkey")).
			SetOwnerSigningPubkey([]byte("test_signing_pubkey")).
			SetSigningKeyshare(signingKeyshare).
			Save(ctx)
		require.NoError(t, err)

		// Create UTXO with sufficient confirmations
		utxoBlockHeight := blockHeight - int(DepositConfirmationThreshold) + 1
		utxo, err := tx.Utxo.Create().
			SetNetwork(schema.NetworkRegtest).
			SetTxid(txid).
			SetVout(vout).
			SetBlockHeight(int64(utxoBlockHeight)).
			SetAmount(1000).
			SetPkScript([]byte("test_script")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		// Test verification
		verifiedUtxo, err := VerifiedTargetUtxo(ctx, tx, schema.NetworkRegtest, txid, vout)
		require.NoError(t, err)
		assert.Equal(t, utxo.ID, verifiedUtxo.ID)
		assert.Equal(t, utxo.BlockHeight, verifiedUtxo.BlockHeight)

		// Test verification in mainnet (should fail)
		_, err = VerifiedTargetUtxo(ctx, tx, schema.NetworkMainnet, txid, vout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "utxo not found")
	})

	t.Run("insufficient confirmations", func(t *testing.T) {
		// Create signing keyshare first
		signingKeyshare, err := tx.SigningKeyshare.Create().
			SetStatus(schema.KeyshareStatusAvailable).
			SetSecretShare([]byte("test_secret_share2")).
			SetPublicShares(map[string][]byte{"test": []byte("test_public_share2")}).
			SetPublicKey([]byte("test_public_key2")).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		// Create deposit address
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress("test_address2").
			SetOwnerIdentityPubkey([]byte("test_identity_pubkey2")).
			SetOwnerSigningPubkey([]byte("test_signing_pubkey2")).
			SetSigningKeyshare(signingKeyshare).
			Save(ctx)
		require.NoError(t, err)

		// Create UTXO with insufficient confirmations
		utxoBlockHeight := blockHeight - int(DepositConfirmationThreshold) + 2
		_, err = tx.Utxo.Create().
			SetNetwork(schema.NetworkRegtest).
			SetTxid([]byte("test_txid2")).
			SetVout(1).
			SetBlockHeight(int64(utxoBlockHeight)).
			SetAmount(1000).
			SetPkScript([]byte("test_script")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		// Test verification
		_, err = VerifiedTargetUtxo(ctx, tx, schema.NetworkRegtest, []byte("test_txid2"), 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deposit tx doesn't have enough confirmations")
	})
}
