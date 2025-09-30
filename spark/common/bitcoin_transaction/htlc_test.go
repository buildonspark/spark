package bitcointransaction

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/txscript"
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
	expectedAddr, err := CreateLightningHTLCTaprootAddressWithSequence(network, hash, hashLockPriv.Public(), LightningHTLCSequence, sequenceLockPriv.Public())
	require.NoError(t, err)
	expectedPkScript, err := txscript.PayToAddrScript(expectedAddr)
	require.NoError(t, err)
	assert.Equal(t, expectedPkScript, htlcTx.TxOut[0].PkScript)
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
	require.Len(t, htlcTx.TxOut, 1)
	assert.Equal(t, amount-int64(fee), htlcTx.TxOut[0].Value)
}

func TestCreateHashLockScript(t *testing.T) {
	hash, err := hex.DecodeString("02d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1")
	require.NoError(t, err)

	pk, err := keys.ParsePublicKeyHex("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
	require.NoError(t, err)

	script, err := CreateHashLockScript(hash, pk)
	require.NoError(t, err)

	require.Equal(
		t,
		"a82002d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1882047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac",
		hex.EncodeToString(script),
	)
}

func TestCreateSequenceLockScript(t *testing.T) {
	pk, err := keys.ParsePublicKeyHex("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
	require.NoError(t, err)

	// Run tests with different sequence values since AddInt64 does some optimizations depending
	// on how it can pack the value.
	tests := []struct {
		name     string
		pubKey   keys.Public
		sequence uint32
		expected string
	}{
		{
			name:     "0 sequence",
			pubKey:   pk,
			sequence: 0,
			expected: "00b2752047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac",
		},
		{
			name:     "< 16 sequence",
			pubKey:   pk,
			sequence: 15,
			expected: "5fb2752047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac",
		},
		{
			name:     "> 16 sequence",
			pubKey:   pk,
			sequence: 2160,
			expected: "027008b2752047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, err := CreateSequenceLockScript(tt.sequence, tt.pubKey)
			require.NoError(t, err)

			require.Equal(
				t,
				tt.expected,
				hex.EncodeToString(script),
			)
		})
	}
}
