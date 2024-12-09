package common

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// Network is the type for Bitcoin networks used with the operator.
type Network int

const (
	// Mainnet is the main Bitcoin network.
	Mainnet Network = iota
	// Regtest is the regression test network.
	Regtest
	// Testnet is the test network.
	Testnet
)

// NetworkParams converts a Network to its corresponding chaincfg.Params
func NetworkParams(network Network) *chaincfg.Params {
	switch network {
	case Mainnet:
		return &chaincfg.MainNetParams
	case Regtest:
		return &chaincfg.RegressionNetParams
	case Testnet:
		return &chaincfg.TestNet3Params
	default:
		return &chaincfg.MainNetParams
	}
}

// P2TRAddressFromPublicKey returns a P2TR address from a public key.
func P2TRAddressFromPublicKey(pubKey []byte, network Network) (*string, error) {
	if len(pubKey) != 33 {
		return nil, fmt.Errorf("public key must be 33 bytes")
	}

	internalKey, err := btcec.ParsePubKey(pubKey)
	if err != nil {
		return nil, err
	}

	// Tweak the internal key with empty merkle root
	taprootKey := txscript.ComputeTaprootKeyNoScript(internalKey)
	taprootAddress, err := btcutil.NewAddressTaproot(
		// Convert a 33 byte public key to a 32 byte x-only public key
		schnorr.SerializePubKey(taprootKey),
		NetworkParams(network),
	)
	if err != nil {
		return nil, err
	}

	addr := taprootAddress.EncodeAddress()
	return &addr, nil
}

// P2TRAddressFromPkScript returns a P2TR address from a public script.
func P2TRAddressFromPkScript(pkScript []byte, network Network) (*string, error) {
	parsedScript, err := txscript.ParsePkScript(pkScript)
	if err != nil {
		return nil, err
	}

	networkParams := NetworkParams(network)
	if parsedScript.Class() == txscript.WitnessV1TaprootTy {
		address, err := parsedScript.Address(networkParams)
		if err != nil {
			return nil, err
		}
		taprootAddress, err := btcutil.NewAddressTaproot(address.ScriptAddress(), networkParams)
		if err != nil {
			return nil, err
		}
		p2trAddress := taprootAddress.String()
		return &p2trAddress, nil
	}

	return nil, fmt.Errorf("not a Taproot address")
}

// TxFromRawTxHex returns a btcd MsgTx from a raw tx hex.
func TxFromRawTxHex(rawTxHex string) (*wire.MsgTx, error) {
	txBytes, err := hex.DecodeString(rawTxHex)
	if err != nil {
		return nil, err
	}
	return TxFromRawTxBytes(txBytes)
}

// TxFromRawTxBytes returns a btcd MsgTx from a raw tx bytes.
func TxFromRawTxBytes(rawTxBytes []byte) (*wire.MsgTx, error) {
	var tx wire.MsgTx
	err := tx.Deserialize(bytes.NewReader(rawTxBytes))
	if err != nil {
		return nil, err
	}
	return &tx, nil
}

// SigHashFromTx returns sighash from a tx.
func SigHashFromTx(tx *wire.MsgTx, inputIndex int, prevOutput *wire.TxOut) ([]byte, error) {
	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(
		prevOutput.PkScript, prevOutput.Value,
	)
	sighashes := txscript.NewTxSigHashes(tx, prevOutputFetcher)

	sigHash, err := txscript.CalcTaprootSignatureHash(sighashes, txscript.SigHashDefault, tx, inputIndex, prevOutputFetcher)
	if err != nil {
		return nil, err
	}
	return sigHash, nil
}
