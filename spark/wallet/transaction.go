package wallet

// Tools for building all the different transactions we use.

import (
	"fmt"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
)

func EphemeralAnchorOutput() *wire.TxOut {
	return wire.NewTxOut(0, []byte{txscript.OP_TRUE, 0x02, 0x4e, 0x73})
}

// maybeApplyFee subtracts the default fee from the amount if it's greater than the fee.
// Returns the original amount if it's less than or equal to the fee.
func maybeApplyFee(amount int64) int64 {
	if amount > int64(common.DefaultFeeSats) {
		return amount - int64(common.DefaultFeeSats)
	}
	return amount
}

func createRootTx(
	depositOutPoint *wire.OutPoint,
	depositTxOut *wire.TxOut,
) *wire.MsgTx {
	rootTx := wire.NewMsgTx(3)
	rootTx.AddTxIn(wire.NewTxIn(depositOutPoint, nil, nil))

	// Create new output with fee-adjusted amount
	rootTx.AddTxOut(wire.NewTxOut(maybeApplyFee(depositTxOut.Value), depositTxOut.PkScript))
	return rootTx
}

func createSplitTx(
	parentOutPoint *wire.OutPoint,
	childTxOuts []*wire.TxOut,
) *wire.MsgTx {
	splitTx := wire.NewMsgTx(3)
	splitTx.AddTxIn(wire.NewTxIn(parentOutPoint, nil, nil))

	// Adjust output amounts to account for fee
	totalOutputAmount := int64(0)
	for _, txOut := range childTxOuts {
		totalOutputAmount += txOut.Value
	}

	if totalOutputAmount > int64(common.DefaultFeeSats) {
		// Distribute fee proportionally across outputs
		feeRatio := float64(common.DefaultFeeSats) / float64(totalOutputAmount)
		for _, txOut := range childTxOuts {
			adjustedAmount := int64(float64(txOut.Value) * (1 - feeRatio))
			splitTx.AddTxOut(wire.NewTxOut(adjustedAmount, txOut.PkScript))
		}
	} else {
		// If fee is larger than total output, just pass through original amounts
		for _, txOut := range childTxOuts {
			splitTx.AddTxOut(txOut)
		}
	}

	return splitTx
}

// createNodeTx creates a node transaction.
// This stands in between a split tx and a leaf node tx,
// and has no timelock.
func createNodeTx(
	parentOutPoint *wire.OutPoint,
	txOut *wire.TxOut,
) *wire.MsgTx {
	newNodeTx := wire.NewMsgTx(3)
	newNodeTx.AddTxIn(wire.NewTxIn(parentOutPoint, nil, nil))

	newNodeTx.AddTxOut(wire.NewTxOut(maybeApplyFee(txOut.Value), txOut.PkScript))
	return newNodeTx
}

// createLeafNodeTx creates a leaf node transaction.
// This transaction provides an intermediate transaction
// to allow the timelock of the final refund transaction
// to be extended. E.g. when the refund tx timelock reaches
// 0, the leaf node tx can be re-signed with a decremented
// timelock, and the refund tx can be reset it's timelock.
func createLeafNodeTx(
	sequence uint32,
	parentOutPoint *wire.OutPoint,
	txOut *wire.TxOut,
	shouldCalculateFee bool,
) *wire.MsgTx {
	newLeafTx := wire.NewMsgTx(3)
	newLeafTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *parentOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence,
	})
	amountSats := txOut.Value
	outputAmount := amountSats
	if shouldCalculateFee {
		outputAmount = maybeApplyFee(amountSats)
	}
	newLeafTx.AddTxOut(wire.NewTxOut(outputAmount, txOut.PkScript))

	return newLeafTx
}

func createRefundTxs(
	sequence uint32,
	nodeOutPoint *wire.OutPoint,
	amountSats int64,
	receivingPubkey *secp256k1.PublicKey,
	shouldCalculateFee bool,
) (*wire.MsgTx, *wire.MsgTx, error) {
	// Create CPFP-friendly refund tx (with ephemeral anchor, no fee)
	cpfpRefundTx := wire.NewMsgTx(3)
	cpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *nodeOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence,
	})

	refundPkScript, err := common.P2TRScriptFromPubKey(receivingPubkey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create refund pkscript: %v", err)
	}
	cpfpRefundTx.AddTxOut(wire.NewTxOut(amountSats, refundPkScript))
	cpfpRefundTx.AddTxOut(EphemeralAnchorOutput())

	// Create direct refund tx (with fee, no anchor)
	directRefundTx := wire.NewMsgTx(3)
	directRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *nodeOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence,
	})

	outputAmount := amountSats
	if shouldCalculateFee {
		outputAmount = maybeApplyFee(amountSats)
	}
	directRefundTx.AddTxOut(wire.NewTxOut(outputAmount, refundPkScript))

	return cpfpRefundTx, directRefundTx, nil
}

func createConnectorRefundTransaction(
	sequence uint32,
	nodeOutPoint *wire.OutPoint,
	connectorOutput *wire.OutPoint,
	amountSats int64,
	receiverPubKey *secp256k1.PublicKey,
) (*wire.MsgTx, error) {
	refundTx := wire.NewMsgTx(3)
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
	return refundTx, nil
}
