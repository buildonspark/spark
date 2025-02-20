package wallet

// Tools for building all the different transactions we use.

import (
	"fmt"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
)

func ephemeralAnchorOutput() *wire.TxOut {
	return wire.NewTxOut(0, []byte{txscript.OP_TRUE})
}

func createRootTx(
	depositOutPoint *wire.OutPoint,
	depositTxOut *wire.TxOut,
) *wire.MsgTx {
	rootTx := wire.NewMsgTx(2)
	rootTx.AddTxIn(wire.NewTxIn(depositOutPoint, nil, nil))
	// We currently send the full value to the same address
	// TODO: 0 fee will only be okay once we add ephemeral anchor outputs
	rootTx.AddTxOut(depositTxOut)
	rootTx.AddTxOut(ephemeralAnchorOutput())
	return rootTx
}

func createSplitTx(
	parentOutPoint *wire.OutPoint,
	childTxOuts []*wire.TxOut,
) *wire.MsgTx {
	splitTx := wire.NewMsgTx(2)
	splitTx.AddTxIn(wire.NewTxIn(parentOutPoint, nil, nil))
	for _, txOut := range childTxOuts {
		splitTx.AddTxOut(txOut)
	}
	splitTx.AddTxOut(ephemeralAnchorOutput())
	return splitTx
}

// createNodeTx creates a node transaction.
// This stands in between a split tx and a leaf node tx,
// and has no timelock.
func createNodeTx(
	parentOutPoint *wire.OutPoint,
	txOut *wire.TxOut,
) *wire.MsgTx {
	newNodeTx := wire.NewMsgTx(2)
	newNodeTx.AddTxIn(wire.NewTxIn(parentOutPoint, nil, nil))
	newNodeTx.AddTxOut(txOut)
	newNodeTx.AddTxOut(ephemeralAnchorOutput())
	return newNodeTx
}

// createLeafNodeTx creates a leaf node transaction.
// This transaction provides an intermediate transaction
// to allow the timelock of the final refund transaction
// to be extended. E.g. when the refund tx timelock reaches
// 0, the leaf node tx can be re-signed with a decremented
// timelock, and the refund tx can be reset it's timelock.
func createLeafNodeTx(
	parentOutPoint *wire.OutPoint,
	txOut *wire.TxOut,
) *wire.MsgTx {
	newLeafTx := wire.NewMsgTx(2)
	newLeafTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *parentOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         spark.InitialSequence(),
	})
	newLeafTx.AddTxOut(txOut)
	newLeafTx.AddTxOut(ephemeralAnchorOutput())
	return newLeafTx
}

func createRefundTx(
	sequence uint32,
	nodeOutPoint *wire.OutPoint,
	amountSats int64,
	receivingPubkey *secp256k1.PublicKey,
) (*wire.MsgTx, error) {
	newRefundTx := wire.NewMsgTx(2)
	newRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *nodeOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence,
	})

	refundPkScript, err := common.P2TRScriptFromPubKey(receivingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund pkscript: %v", err)
	}
	newRefundTx.AddTxOut(wire.NewTxOut(amountSats, refundPkScript))
	newRefundTx.AddTxOut(ephemeralAnchorOutput())

	return newRefundTx, nil
}

func createConnectorRefundTransaction(
	sequence uint32,
	nodeOutPoint *wire.OutPoint,
	connectorOutput *wire.OutPoint,
	amountSats int64,
	receiverPubKey *secp256k1.PublicKey,
) (*wire.MsgTx, error) {
	refundTx := wire.NewMsgTx(2)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *nodeOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence,
	})
	refundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))
	receiverScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create receiver script: %v", err)
	}
	refundTx.AddTxOut(wire.NewTxOut(amountSats, receiverScript))
	refundTx.AddTxOut(ephemeralAnchorOutput())
	return refundTx, nil
}
