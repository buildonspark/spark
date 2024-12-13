package testutil

import (
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// CreateTestP2TRTransaction creates a test P2TR transaction with a dummy input and output.
func CreateTestP2TRTransaction(p2trAddress string, amountSats int64) (*wire.MsgTx, error) {
	// Create new transaction
	tx := wire.NewMsgTx(wire.TxVersion)

	// Add a dummy input
	prevOut := wire.NewOutPoint(&chainhash.Hash{}, 0) // Empty hash and index 0
	txIn := wire.NewTxIn(prevOut, nil, [][]byte{})

	// For taproot, we need some form of witness data
	// This is just dummy data for testing
	txIn.Witness = wire.TxWitness{
		[]byte{}, // Empty witness element as placeholder
	}
	tx.AddTxIn(txIn)

	// Decode the P2TR address
	addr, err := btcutil.DecodeAddress(p2trAddress, &chaincfg.MainNetParams)
	if err != nil {
		return nil, fmt.Errorf("error decoding address: %v", err)
	}

	// Create P2TR output script
	pkScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return nil, fmt.Errorf("error creating output script: %v", err)
	}

	// Create the output
	txOut := wire.NewTxOut(amountSats, pkScript)
	tx.AddTxOut(txOut)

	return tx, nil
}
