package wallet

// Tools for building all the different transactions we use.

import (
	"bytes"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
)

func createRootTx(
	depositOutPoint *wire.OutPoint,
	depositTxOut *wire.TxOut,
) *wire.MsgTx {
	rootTx := wire.NewMsgTx(3)
	rootTx.AddTxIn(wire.NewTxIn(depositOutPoint, nil, nil))

	// Create new output with fee-adjusted amount
	rootTx.AddTxOut(wire.NewTxOut(depositTxOut.Value, depositTxOut.PkScript))
	return rootTx
}

// CreateLeafNodeTx creates a leaf node transaction.
// This transaction provides an intermediate transaction
// to allow the timelock of the final refund transaction
// to be extended. E.g. when the refund tx timelock reaches
// 0, the leaf node tx can be re-signed with a decremented
// timelock, and the refund tx can be reset it's timelock.
func CreateLeafNodeTx(
	sequence uint32,
	parentOutPoint *wire.OutPoint,
	txOut *wire.TxOut,
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
	newLeafTx.AddTxOut(wire.NewTxOut(outputAmount, txOut.PkScript))
	return newLeafTx
}

func CreateRefundTxs(
	sequence uint32,
	directSequence uint32,
	nodeOutPoint *wire.OutPoint,
	amountSats int64,
	receivingPubkey keys.Public,
	shouldCalculateFee bool,
) (*wire.MsgTx, *wire.MsgTx, error) {
	cpfpRefundTx, directFromCpfpRefundTx, _, err := CreateAllRefundTxs(sequence, directSequence, nodeOutPoint, amountSats, nil, 0, receivingPubkey, shouldCalculateFee)
	return cpfpRefundTx, directFromCpfpRefundTx, err
}

// CreateAllRefundTxs creates all three refund transaction types:
// 1. cpfpRefundTx: Spends from nodeOutPoint, has ephemeral anchor, no fee
// 2. directFromCpfpRefundTx: Spends from nodeOutPoint, has fee, no anchor
// 3. directRefundTx: Spends from directNodeOutPoint (if provided), has fee, no anchor
func CreateAllRefundTxs(
	sequence uint32,
	directSequence uint32,
	nodeOutPoint *wire.OutPoint,
	nodeAmountSats int64,
	directNodeOutPoint *wire.OutPoint, // nil if no DirectNodeTx exists
	directNodeAmountSats int64, // 0 if no DirectNodeTx
	receivingPubkey keys.Public,
	shouldCalculateFee bool,
) (*wire.MsgTx, *wire.MsgTx, *wire.MsgTx, error) {
	refundPkScript, err := common.P2TRScriptFromPubKey(receivingPubkey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create refund pkscript: %w", err)
	}

	// Create CPFP-friendly refund tx (with ephemeral anchor, no fee)
	cpfpRefundTx := wire.NewMsgTx(3)
	cpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *nodeOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence,
	})
	cpfpRefundTx.AddTxOut(wire.NewTxOut(nodeAmountSats, refundPkScript))
	cpfpRefundTx.AddTxOut(common.EphemeralAnchorOutput())

	// Create DirectFromCpfpRefundTx (spending from NodeTx/CPFP, with fee, no anchor)
	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *nodeOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence + spark.DirectTimelockOffset,
	})
	outputAmount := nodeAmountSats
	if shouldCalculateFee {
		outputAmount = common.MaybeApplyFee(nodeAmountSats)
	}
	directFromCpfpRefundTx.AddTxOut(wire.NewTxOut(outputAmount, refundPkScript))

	// Create DirectRefundTx (spending from DirectNodeTx, with fee, no anchor)
	var directRefundTx *wire.MsgTx
	if directNodeOutPoint != nil {
		directRefundTx = wire.NewMsgTx(3)
		directRefundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: *directNodeOutPoint,
			SignatureScript:  nil,
			Witness:          nil,
			Sequence:         sequence + spark.DirectTimelockOffset,
		})
		directOutputAmount := directNodeAmountSats
		if shouldCalculateFee {
			directOutputAmount = common.MaybeApplyFee(directNodeAmountSats)
		}
		directRefundTx.AddTxOut(wire.NewTxOut(directOutputAmount, refundPkScript))
	}

	return cpfpRefundTx, directFromCpfpRefundTx, directRefundTx, nil
}

func createConnectorRefundTransaction(
	sequence uint32,
	nodeOutPoint *wire.OutPoint,
	connectorOutput *wire.OutPoint,
	amountSats int64,
	receiverPubKey keys.Public,
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
		return nil, fmt.Errorf("failed to create receiver script: %w", err)
	}
	refundTx.AddTxOut(wire.NewTxOut(amountSats, receiverScript))
	return refundTx, nil
}

func SerializeTx(tx *wire.MsgTx) ([]byte, error) {
	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
