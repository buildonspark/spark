package wallet

// Tools for building all the different transactions we use.

import (
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
)

func createRootTx(
	depositOutPoint *wire.OutPoint,
	depositTxOut *wire.TxOut,
) *wire.MsgTx {
	rootTx := wire.NewMsgTx(2)
	rootTx.AddTxIn(wire.NewTxIn(depositOutPoint, nil, nil))
	// We currently send the full value to the same address
	// TODO: 0 fee will only be okay once we add ephemeral anchor outputs
	rootTx.AddTxOut(depositTxOut)
	return rootTx
}

func nextSequence(currSequence uint32) (uint32, error) {
	if currSequence&0xFFFF-spark.TimeLockInterval <= 0 {
		return 0, fmt.Errorf("timelock interval is less or equal to 0")
	}
	return uint32((1 << 30) | (currSequence&0xFFFF - spark.TimeLockInterval)), nil
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
	return refundTx, nil
}
