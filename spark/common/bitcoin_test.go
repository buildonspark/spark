package common

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/go-playground/assert/v2"
	"github.com/stretchr/testify/require"
)

func TestP2TRAddressFromPublicKey(t *testing.T) {
	testVectors := []struct {
		pubKeyHex string
		p2trAddr  string
		network   Network
	}{
		{"0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", "bc1pmfr3p9j00pfxjh0zmgp99y8zftmd3s5pmedqhyptwy6lm87hf5sspknck9", Mainnet},
		{"03797dd653040d344fd048c1ad05d4cbcb2178b30c6a0c4276994795f3e833da41", "tb1p8dlmzllfah294ntwatr8j5uuvcj7yg0dete94ck2krrk0ka2c9qqex96hv", Testnet},
	}

	for _, tv := range testVectors {
		pubKey, err := hex.DecodeString(tv.pubKeyHex)
		if err != nil {
			t.Fatalf("Failed to decode public key: %v", err)
		}

		addr, err := P2TRAddressFromPublicKey(pubKey, tv.network)
		if err != nil {
			t.Fatalf("Failed to get P2TR address: %v", err)
		}

		if *addr != tv.p2trAddr {
			t.Fatalf("P2TR address mismatch: got %s, want %s", *addr, tv.p2trAddr)
		}
	}
}

func TestP2TRAddressFromPkScript(t *testing.T) {
	testVectors := []struct {
		pkScriptHex string
		p2trAddr    string
		network     Network
	}{
		{"51206d2a651074ff19686d4cd4e45aaaad3f85639e90bb24e21b875b174b0635eb30", "bc1pd54x2yr5luvksm2v6nj9424d87zk885shvjwyxu8tvt5kp34avcq024v6k", Mainnet},
		{"5120d0cd6fade9979fc9e0cc353d8e06a22f43d659cf09c8f909834e80468f4af966", "bcrt1p6rxklt0fj70uncxvx57cup4z9apavkw0p8y0jzvrf6qydr62l9nqd94jkz", Regtest},
	}

	for _, tv := range testVectors {
		pkScript, err := hex.DecodeString(tv.pkScriptHex)
		if err != nil {
			t.Fatalf("Failed to decode pubkey script: %v", err)
		}

		addr, err := P2TRAddressFromPkScript(pkScript, tv.network)
		if err != nil {
			t.Fatalf("Failed to get P2TR address: %v", err)
		}

		if *addr != tv.p2trAddr {
			t.Fatalf("P2TR address mismatch: got %s, want %s", *addr, tv.p2trAddr)
		}
	}
}

func TestTxFromRawTxHex(t *testing.T) {
	rawTxHex := "02000000000102dc552c6c0ef5ed0d8cd64bd1d2d1ffd7cf0ec0b5ad8df2a4c6269b59cffcc696010000000000000000603fbd40e86ee82258c57571c557b89a444aabf5b6a05574e6c6848379febe9a00000000000000000002e86905000000000022512024741d89092c5965f35a63802352fa9c7fae4a23d471b9dceb3379e8ff6b7dd1d054080000000000220020aea091435e74e3c1eba0bd964e67a05f300ace9e73efa66fe54767908f3e68800140f607486d87f59af453d62cffe00b6836d8cca2c89a340fab5fe842b20696908c77fd2f64900feb0cbb1c14da3e02271503fc465fcfb1b043c8187dccdd494558014067dff0f0c321fc8abc28bf555acfdfa5ee889b6909b24bc66cedf05e8cc2750a4d95037c3dc9c24f1e502198bade56fef61a2504809f5b2a60a62afeaf8bf52e00000000"
	_, err := TxFromRawTxHex(rawTxHex)
	if err != nil {
		t.Fatalf("Failed to decode raw transaction: %v", err)
	}
}

func TestSigHashFromTx(t *testing.T) {
	prevTx, _ := TxFromRawTxHex("020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000")

	tx := wire.NewMsgTx(2)
	txIn := wire.NewTxIn(
		&wire.OutPoint{Hash: prevTx.TxHash(), Index: 0},
		nil,
		nil,
	)
	tx.AddTxIn(txIn)

	txOut := wire.NewTxOut(70_000, prevTx.TxOut[0].PkScript)
	tx.AddTxOut(txOut)

	sighash, _ := SigHashFromTx(tx, 0, prevTx.TxOut[0])

	if hex.EncodeToString(sighash) != "8da5e7aa2b03491d7c2f4359ea4968dd58f69adf9af1a2c6881be0295591c293" {
		t.Fatalf("Sighash mismatch")
	}
}

func TestVerifySignature(t *testing.T) {
	privKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	pubKey := privKey.PubKey()
	addr, err := P2TRAddressFromPublicKey(pubKey.SerializeCompressed(), Regtest)
	require.NoError(t, err)
	address, err := btcutil.DecodeAddress(*addr, &chaincfg.RegressionNetParams)
	require.NoError(t, err)
	script, _ := txscript.PayToAddrScript(address)
	require.NoError(t, err)

	creditTx := wire.NewMsgTx(2)
	txOut := wire.NewTxOut(100_000, script)
	creditTx.AddTxOut(txOut)

	debitTx := wire.NewMsgTx(2)
	txIn := wire.NewTxIn(
		&wire.OutPoint{Hash: creditTx.TxHash(), Index: 0},
		nil,
		nil,
	)
	debitTx.AddTxIn(txIn)
	newTxOut := wire.NewTxOut(99_000, script)
	debitTx.AddTxOut(newTxOut)

	sighash, err := SigHashFromTx(debitTx, 0, creditTx.TxOut[0])
	require.NoError(t, err)
	// secp vs. schnorr.sign...?
	taprootKey := txscript.TweakTaprootPrivKey(*privKey, []byte{})
	sig, err := schnorr.Sign(taprootKey, sighash)
	require.NoError(t, err)
	require.True(t, sig.Verify(sighash, taprootKey.PubKey()))
	var debitTxBuf bytes.Buffer
	err = debitTx.Serialize(&debitTxBuf)
	require.NoError(t, err)

	signedDebitTxBytes, err := UpdateTxWithSignature(debitTxBuf.Bytes(), 0, sig.Serialize())
	require.NoError(t, err)
	signedDebitTx, err := TxFromRawTxBytes(signedDebitTxBytes)
	require.NoError(t, err)

	err = VerifySignatureSingleInput(signedDebitTx, 0, creditTx.TxOut[0])
	require.NoError(t, err, "signature verification failed: %v", err)
}

func TestSerializeTx(t *testing.T) {
	txString := "0200000000010109c67bcd9d9276e8cf6213eb1b75dc029633df65f7cfb204004156876ff4acb60000000000ffffffff01905f0100000000002251208df27e8cea291091c22bc4ae6e5a8e9d3b9b4905f08bcebb499ab752374cfa3201407713a006ee2db39cc2eca2c83a9d41b6b18c8116dda3306c588f1cbc37fd681da26bf09db67cc297581269a3e8da1b00df7abb12ac8716d2c86f22e3dfc0cc1c00000000"
	tx, err := TxFromRawTxHex(txString)
	require.NoError(t, err)
	serializedTx, err := SerializeTx(tx)
	require.NoError(t, err)
	assert.Equal(t, txString, hex.EncodeToString(serializedTx))
}
