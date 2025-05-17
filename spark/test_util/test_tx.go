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
	inputs := []*wire.TxIn{dummyInput()}
	txOut, err := createP2TROutput(p2trAddress, amountSats)
	if err != nil {
		return nil, fmt.Errorf("error creating output: %v", err)
	}
	outputs := []*wire.TxOut{txOut}
	return CreateTestTransaction(inputs, outputs), nil
}

// CreateTestDepositTransaction creates a test deposit transaction spending
// the given outpoint to the given P2TR address with the given amount.
func CreateTestDepositTransaction(outPoint *wire.OutPoint, p2trAddress string, amountSats int64) (*wire.MsgTx, error) {
	inputs := []*wire.TxIn{wire.NewTxIn(outPoint, nil, [][]byte{})}
	txOut, err := createP2TROutput(p2trAddress, amountSats)
	if err != nil {
		return nil, fmt.Errorf("error creating output: %v", err)
	}
	outputs := []*wire.TxOut{txOut}
	return CreateTestTransaction(inputs, outputs), nil
}

// CreateTestDepositTransactionManyOutputs creates a test deposit transaction spending
// the given outpoint to the given P2TR addresses with the given amount.
func CreateTestDepositTransactionManyOutputs(outPoint *wire.OutPoint, p2trAddresses []string, amountSats int64) (*wire.MsgTx, error) {
	inputs := []*wire.TxIn{wire.NewTxIn(outPoint, nil, [][]byte{})}
	outputs := make([]*wire.TxOut, 0)
	for _, p2trAddress := range p2trAddresses {
		txOut, err := createP2TROutput(p2trAddress, amountSats)
		if err != nil {
			return nil, fmt.Errorf("error creating output: %v", err)
		}
		outputs = append(outputs, txOut)
	}
	return CreateTestTransaction(inputs, outputs), nil
}

// CreateTestCoopExitTransaction creates a test coop exit transaction with a dummy input and two outputs.
// The first output is for the user and the second output is for the intermediate tx spending
// to connector outputs. See `CreateTestConnectorTransaction` for the intermediate tx.
func CreateTestCoopExitTransaction(
	outPoint *wire.OutPoint,
	userP2trAddr string, userAmountSats int64, intermediateP2trAddr string, intermediateAmountSats int64,
) (*wire.MsgTx, error) {
	inputs := []*wire.TxIn{wire.NewTxIn(outPoint, nil, [][]byte{})}
	userOutput, err := createP2TROutput(userP2trAddr, userAmountSats)
	if err != nil {
		return nil, fmt.Errorf("error creating output: %v", err)
	}
	intermediateOutput, err := createP2TROutput(intermediateP2trAddr, intermediateAmountSats)
	if err != nil {
		return nil, fmt.Errorf("error creating output: %v", err)
	}
	outputs := []*wire.TxOut{userOutput, intermediateOutput}
	return CreateTestTransaction(inputs, outputs), nil
}

// CreateTestConnectorTransaction creates a tx that
// spends an output on the coop exit transaction, to connector outputs.
// This allows for the SSP to pay the fees to put the connector outputs
// on-chain only in the unhappy case, instead of the user.
func CreateTestConnectorTransaction(
	intermediateOutPoint *wire.OutPoint, intermediateAmountSats int64, connectorP2trAddrs []string, feeBumpP2trAddr string,
) (*wire.MsgTx, error) {
	inputs := []*wire.TxIn{wire.NewTxIn(intermediateOutPoint, nil, [][]byte{})}
	outputAddrs := append(connectorP2trAddrs, feeBumpP2trAddr)
	outputAmountSats := intermediateAmountSats / int64(len(connectorP2trAddrs)) // Should be dust, i.e. 354 sats
	outputs := make([]*wire.TxOut, 0)
	for _, addr := range outputAddrs {
		connectorOutput, err := createP2TROutput(addr, outputAmountSats)
		if err != nil {
			return nil, fmt.Errorf("error creating output: %v", err)
		}
		outputs = append(outputs, connectorOutput)
	}
	return CreateTestTransaction(inputs, outputs), nil
}

func CreateTestTransaction(inputs []*wire.TxIn, outputs []*wire.TxOut) *wire.MsgTx {
	tx := wire.NewMsgTx(3)
	for _, in := range inputs {
		tx.AddTxIn(in)
	}
	for _, out := range outputs {
		tx.AddTxOut(out)
	}
	return tx
}

func dummyInput() *wire.TxIn {
	prevOut := wire.NewOutPoint(&chainhash.Hash{}, 0) // Empty hash and index 0
	txIn := wire.NewTxIn(prevOut, nil, [][]byte{})

	// For taproot, we need some form of witness data
	// This is just dummy data for testing
	txIn.Witness = wire.TxWitness{
		[]byte{}, // Empty witness element as placeholder
	}

	return txIn
}

func createP2TROutput(p2trAddress string, amountSats int64) (*wire.TxOut, error) {
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
	return wire.NewTxOut(amountSats, pkScript), nil
}
