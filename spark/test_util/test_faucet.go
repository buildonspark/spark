package testutil

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/stretchr/testify/assert"
)

// FundFaucet mines a block and sends some coins to a taproot address,
// and mines 100 blocks to pass coinbase maturity.
// Returns the private key to sign with, and the output/outpoint
// that's spendable with this key.
func FundFaucet(t *testing.T, client *rpcclient.Client) (*secp256k1.PrivateKey, *wire.TxOut, *wire.OutPoint) {
	// Mine a block sending some coins to an address
	sspOnChainKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	sspOnChainPubKey := sspOnChainKey.PubKey()
	sspOnChainAddress, err := common.P2TRRawAddressFromPublicKey(sspOnChainPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	blockHash, err := client.GenerateToAddress(1, sspOnChainAddress, nil)
	assert.NoError(t, err)

	// Get the tx, and check the output matches the expected script/pubkey
	sspOnChainScript, err := txscript.PayToAddrScript(sspOnChainAddress)
	assert.NoError(t, err)

	block, err := client.GetBlockVerboseTx(blockHash[0])
	assert.NoError(t, err)
	fundingTx := block.Tx[0]
	assert.Equal(t, 2, len(fundingTx.Vout))
	observedScript, err := hex.DecodeString(fundingTx.Vout[0].ScriptPubKey.Hex)
	assert.NoError(t, err)

	assert.Equal(t, sspOnChainScript, observedScript)

	// Extract the pubkey from the script and check it matches the one we expect
	assert.Equal(t, 34, len(observedScript))
	observedPubkey, err := secp256k1.ParsePubKey(append([]byte{0x02}, observedScript[2:34]...))
	if err != nil {
		observedPubkey, err = secp256k1.ParsePubKey(append([]byte{0x03}, observedScript[2:34]...))
	}
	assert.NoError(t, err)

	taprootKey := txscript.TweakTaprootPrivKey(*sspOnChainKey, []byte{})
	assert.Equal(t, taprootKey.PubKey().SerializeCompressed()[1:], observedPubkey.SerializeCompressed()[1:])

	// Generate 100 blocks to allow ssp funds to be spendable
	randomKey, err := secp256k1.GeneratePrivateKey()
	assert.NoError(t, err)
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	assert.NoError(t, err)
	_, err = client.GenerateToAddress(100, randomAddress, nil)
	assert.NoError(t, err)

	// Craft the output and outpoint to spend this output
	fundingTxOut := wire.NewTxOut(int64(fundingTx.Vout[0].Value*100_000_000), observedScript)

	fundingTxid, err := chainhash.NewHashFromStr(fundingTx.Txid)
	assert.NoError(t, err)
	fundingOutPoint := wire.NewOutPoint(fundingTxid, 0)

	return sspOnChainKey, fundingTxOut, fundingOutPoint
}

// SignOnChainTx signs the first input of the given transaction with the given key,
// and returns the signed transaction. Note this expects to be spending
// a taproot output, mainly created by `fundFaucet`.
func SignOnChainTx(t *testing.T, unsignedTx *wire.MsgTx, fundingTxOut *wire.TxOut, sspOnChainKey *secp256k1.PrivateKey) *wire.MsgTx {
	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(
		fundingTxOut.PkScript, fundingTxOut.Value,
	)
	sighashes := txscript.NewTxSigHashes(unsignedTx, prevOutputFetcher)
	fakeTapscriptRootHash := []byte{}
	sig, err := txscript.RawTxInTaprootSignature(
		unsignedTx, sighashes, 0, fundingTxOut.Value, fundingTxOut.PkScript,
		fakeTapscriptRootHash, txscript.SigHashDefault, sspOnChainKey,
	)
	assert.NoError(t, err)

	var exitTxBuf bytes.Buffer
	err = unsignedTx.Serialize(&exitTxBuf)
	assert.NoError(t, err)

	signedExitTxBytes, err := common.UpdateTxWithSignature(exitTxBuf.Bytes(), 0, sig)
	assert.NoError(t, err)
	signedExitTx, err := common.TxFromRawTxBytes(signedExitTxBytes)
	assert.NoError(t, err)

	err = common.VerifySignature(signedExitTx, 0, fundingTxOut)
	assert.NoError(t, err)

	return signedExitTx
}
