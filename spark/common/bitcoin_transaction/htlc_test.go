package bitcointransaction

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateLightningHTLCTransaction_BuildsExpectedTx(t *testing.T) {
	// Arrange
	network := common.Regtest
	hash := bytes.Repeat([]byte{0x11}, 32)
	hashLockPriv, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	sequenceLockPriv, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

	// Build a simple node tx with one input and one output
	parentOutPoint := &wire.OutPoint{}
	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(wire.NewTxIn(parentOutPoint, nil, nil))
	amount := int64(100_000)
	nodeTx.AddTxOut(wire.NewTxOut(amount, []byte{0x51})) // OP_TRUE as placeholder

	sequence := uint32(12345)

	// Act
	htlcTx, err := CreateLightningHTLCTransaction(
		nodeTx,
		0,
		network,
		sequence,
		hash,
		hashLockPriv.Public(),
		sequenceLockPriv.Public(),
	)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, htlcTx)
	// 1 input, 2 outputs (HTLC + ephemeral anchor)
	require.Len(t, htlcTx.TxIn, 1)
	require.Len(t, htlcTx.TxOut, 2)
	// Input prev outpoint and sequence propagated
	outpoint := wire.OutPoint{
		Hash:  nodeTx.TxHash(),
		Index: 0,
	}
	assert.Equal(t, outpoint, htlcTx.TxIn[0].PreviousOutPoint)
	assert.Equal(t, sequence, htlcTx.TxIn[0].Sequence)
	// First output amount preserved (no fee in CPFP-friendly variant)
	assert.Equal(t, amount, htlcTx.TxOut[0].Value)
	// First output script matches computed HTLC taproot address script
	expectedAddr, err := CreateLightningHTLCTaprootAddress(network, hash, hashLockPriv.Public(), sequenceLockPriv.Public())
	require.NoError(t, err)
	assert.Equal(t, expectedAddr.ScriptAddress(), htlcTx.TxOut[0].PkScript)
	// Second output is the ephemeral anchor (zero-value, fixed script)
	anchor := common.EphemeralAnchorOutput()
	assert.Equal(t, int64(0), htlcTx.TxOut[1].Value)
	assert.Equal(t, anchor.PkScript, htlcTx.TxOut[1].PkScript)
}

func TestCreateDirectLightningHTLCTransaction_SubtractsFee(t *testing.T) {
	// Arrange
	network := common.Regtest
	hash := bytes.Repeat([]byte{0x22}, 32)
	hashLockPriv, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	sequenceLockPriv, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

	parentOutPoint := &wire.OutPoint{}
	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(wire.NewTxIn(parentOutPoint, nil, nil))
	amount := int64(50_000)
	nodeTx.AddTxOut(wire.NewTxOut(amount, []byte{0x51}))

	sequence := uint32(54321)
	fee := common.DefaultFeeSats

	// Act
	htlcTx, err := CreateDirectLightningHTLCTransaction(
		nodeTx,
		0,
		network,
		sequence,
		hash,
		hashLockPriv.Public(),
		sequenceLockPriv.Public(),
	)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, htlcTx)
	require.Len(t, htlcTx.TxOut, 2)
	assert.Equal(t, amount-int64(fee), htlcTx.TxOut[0].Value)
}
