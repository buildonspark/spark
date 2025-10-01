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
	hashLockPriv := keys.GeneratePrivateKey()
	sequenceLockPriv := keys.GeneratePrivateKey()

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
	hashLockPriv := keys.GeneratePrivateKey()
	sequenceLockPriv := keys.GeneratePrivateKey()

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

func TestCreateLightningHTLCTransaction_BuildsExpectedTxFromExpectedParams(t *testing.T) {
	network := common.Regtest

	rawTxHex := "0300000000010180d6e3ba8082893627a42f2770fdb2e900731638258a2d04cd6b8b2f7a982e150000000000d0070040020002000000000000225120d04e30f634945d8b59283c10831cfab354d6d9cb88d1f7adfdba67cb8a7734f500000000000000000451024e730140ebcc474fdc71b83fe5f547976e418e91025ef8b323b572f68e709b82c36c7303496ee315c3b3b710af59c14f8d2aa97b9a0bc40b778385b32c59f7e0f34fabb200000000"
	rawTx, err := common.TxFromRawTxHex(rawTxHex)
	require.NoError(t, err)

	vout := uint32(0)

	rawRefundTxHex := "03000000000101d4b9193b8a28d4a986a15f17f5fe4e310c1d73e34865a24d04d39e37dddaccff00000000006c0700000200020000000000002251200686f6870264df6673c066f0591d38b5d60636f4f7a58143b88cbdff327cb68000000000000000000451024e73014003bb8cccc5b494ac9eb2b510618e5c54bd0082c5c5ba0838c9411f3d432dd4a0ec59ec4b4274006a2761040d8aa54702bc01dfed165035c2beaa017e5acc79c100000000"
	rawRefundTx, err := common.TxFromRawTxHex(rawRefundTxHex)
	require.NoError(t, err)

	hash, err := hex.DecodeString("10d31aeabd2bf7cdcba3a229107a4edb7b1c5b35c90c2fca491bd127c68069bd")
	require.NoError(t, err)

	hashLockPubKey, err := keys.ParsePublicKeyHex("028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4")
	require.NoError(t, err)

	// We could just hardcode this value, but in the spirit of making sure everything is derived
	// from "real" transactions in this test, derive it from the refund tx.
	sequence := rawRefundTx.TxIn[0].Sequence - 30

	sequenceLockPubKey, err := keys.ParsePublicKeyHex("032f0db1a8b99ad42e75e2f1cf4d977511a6d94587b4482c77fbd1fe9acc456a27")
	require.NoError(t, err)

	htlcTx, err := CreateLightningHTLCTransaction(rawTx, vout, network, sequence, hash, hashLockPubKey, sequenceLockPubKey)
	require.NoError(t, err)

	htlcTxHex, err := common.SerializeTxHex(htlcTx)
	require.NoError(t, err)

	expectedTxWithWitnessHex := "03000000000101d4b9193b8a28d4a986a15f17f5fe4e310c1d73e34865a24d04d39e37dddaccff00000000004e0700000200020000000000002251207898ca6a523e1724e99e3f6eb9bbd36eba16e6b15304921854e3c6b1174574b200000000000000000451024e7301406edf601068e37dc1222de88f2cbceaf9bcaa391683a7f393a40a68dc37d8765a7fae02793c4c3981101d6f35a9b9cd3a901c5f109f58a76ffaf8838c80670b5900000000"
	expectedTxWithWitness, err := common.TxFromRawTxHex(expectedTxWithWitnessHex)
	require.NoError(t, err)

	expectedTxHex, err := common.SerializeTxNoWitnessHex(expectedTxWithWitness)
	require.NoError(t, err)

	require.Equal(t,
		expectedTxHex,
		htlcTxHex,
	)
}

func TestCreateHTLCTaprootAddress(t *testing.T) {
	hash, err := hex.DecodeString("02d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1")
	require.NoError(t, err)

	pk1, err := keys.ParsePublicKeyHex("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
	require.NoError(t, err)

	pk2, err := keys.ParsePublicKeyHex("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
	require.NoError(t, err)

	address, err := CreateLightningHTLCTaprootAddressWithSequence(common.Regtest, hash, pk1, 2160, pk2)
	require.NoError(t, err)

	require.Equal(t, "bcrt1p0kdvjnm6mz6zzhnkxhhdw6gemt9cjyvmnn48evlfx7s9hn3a8dxqq7g3eg", address.String())
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
