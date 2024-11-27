package common

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
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
