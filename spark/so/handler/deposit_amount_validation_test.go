package handler

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestDepositAddress creates a minimal DepositAddress with its required
// SigningKeyshare edge, suitable for linking to Utxo records in tests.
func createTestDepositAddress(t *testing.T, ctx context.Context, client *ent.Client) *ent.DepositAddress {
	t.Helper()
	secret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"k": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	addr, err := client.DepositAddress.Create().
		SetAddress("test_deposit_addr").
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetSigningKeyshare(keyshare).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)
	return addr
}

// TestValidateDepositUtxoValueAgainstChain_RejectsInflatedValue verifies that
// the cross-check against the chain watcher's Utxo table rejects a client that
// claims a higher deposit value than the actual on-chain UTXO.
func TestValidateDepositUtxoValueAgainstChain_RejectsInflatedValue(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	depositAddr := createTestDepositAddress(t, ctx, client)

	// Build a fake on-chain TX with an inflated output value.
	realValue := int64(50_000)
	inflatedValue := int64(500_000)

	onChainTx := wire.NewMsgTx(2)
	onChainTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}})
	onChainTx.AddTxOut(&wire.TxOut{Value: inflatedValue, PkScript: []byte{0x51, 0x20, 0x00}})

	txidHash := onChainTx.TxHash()
	txidBytes, err := hex.DecodeString(txidHash.String())
	require.NoError(t, err)

	// Record the UTXO in the chain watcher table with the real (lower) value.
	_, err = client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txidBytes).
		SetVout(0).
		SetBlockHeight(100).
		SetAmount(uint64(realValue)).
		SetPkScript([]byte{0x51, 0x20, 0x00}).
		SetDepositAddress(depositAddr).
		Save(ctx)
	require.NoError(t, err)

	// The validation should reject because inflatedValue > realValue.
	err = validateDepositUtxoValueAgainstChain(
		ctx, client, btcnetwork.Regtest,
		onChainTx, 0, onChainTx.TxOut[0],
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds on-chain UTXO value")
}

// TestValidateDepositUtxoValueAgainstChain_AcceptsMatchingValue verifies that
// a deposit with a value matching the on-chain UTXO is accepted.
func TestValidateDepositUtxoValueAgainstChain_AcceptsMatchingValue(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	depositAddr := createTestDepositAddress(t, ctx, client)

	value := int64(50_000)

	onChainTx := wire.NewMsgTx(2)
	onChainTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}})
	onChainTx.AddTxOut(&wire.TxOut{Value: value, PkScript: []byte{0x51, 0x20, 0x00}})

	txidHash := onChainTx.TxHash()
	txidBytes, err := hex.DecodeString(txidHash.String())
	require.NoError(t, err)

	_, err = client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txidBytes).
		SetVout(0).
		SetBlockHeight(100).
		SetAmount(uint64(value)).
		SetPkScript([]byte{0x51, 0x20, 0x00}).
		SetDepositAddress(depositAddr).
		Save(ctx)
	require.NoError(t, err)

	err = validateDepositUtxoValueAgainstChain(
		ctx, client, btcnetwork.Regtest,
		onChainTx, 0, onChainTx.TxOut[0],
	)
	require.NoError(t, err)
}

// TestValidateDepositUtxoValueAgainstChain_SkipsWhenNotRecorded verifies that
// if the chain watcher hasn't recorded the UTXO yet, the check is skipped
// (tree will stay Pending until confirmation).
func TestValidateDepositUtxoValueAgainstChain_SkipsWhenNotRecorded(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	onChainTx := wire.NewMsgTx(2)
	onChainTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{0x01}}})
	onChainTx.AddTxOut(&wire.TxOut{Value: 999_999, PkScript: []byte{0x51, 0x20, 0x00}})

	// No UTXO recorded in DB -- should pass without error.
	err = validateDepositUtxoValueAgainstChain(
		ctx, client, btcnetwork.Regtest,
		onChainTx, 0, onChainTx.TxOut[0],
	)
	require.NoError(t, err)
}
