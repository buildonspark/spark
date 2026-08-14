package handler

import (
	"bytes"
	"context"
	"fmt"
	"math"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/sighash"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttree "github.com/lightsparkdev/spark/so/ent/tree"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
)

// recoverableOutput is a leaf's value in a confirmed on-chain output only the
// leaf's key can spend, with the transaction spending it.
type recoverableOutput struct {
	// The node whose direct tx created the output. Logged, not returned: it is an
	// ancestor generation of the caller's leaf.
	sourceNodeID uuid.UUID
	prevOut      *wire.TxOut
	sighash      sighash.Hash
}

// resolveRecoverableOutput validates a recovery request against this operator's
// own rows. Runs identically in Prepare on every SO and on the coordinator for a
// re-sign, so the two cannot diverge. Needs no chain query: MarkExitingNodes
// already stored the confirming transaction and its height on the node row.
func resolveRecoverableOutput(ctx context.Context, db *ent.Client, leaf *ent.TreeNode, rawTx []byte) (*recoverableOutput, error) {
	if len(rawTx) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("recovery transaction is empty"))
	}
	recoveryTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse recovery transaction: %w", err))
	}
	// The sighash below is taken at index 0, and a second input would need a
	// prevout this operator cannot know.
	if len(recoveryTx.TxIn) != 1 {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("recovery transaction must have exactly 1 input, got %d", len(recoveryTx.TxIn)))
	}
	if len(recoveryTx.TxOut) == 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("recovery transaction has no outputs"))
	}

	outPoint := recoveryTx.TxIn[0].PreviousOutPoint
	sourceNode, err := findRecoverySourceNode(ctx, db, leaf, outPoint)
	if err != nil {
		return nil, err
	}

	sourceTx, err := common.TxFromRawTxBytes(sourceNode.DirectTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse direct tx of node %s: %w", sourceNode.ID, err)
	}
	if outPoint.Index >= uint32(len(sourceTx.TxOut)) {
		return nil, sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("outpoint %s names output %d but its transaction has %d", outPoint, outPoint.Index, len(sourceTx.TxOut)))
	}
	prevOut := sourceTx.TxOut[outPoint.Index]

	// The check the whole endpoint rests on: node outputs pay
	// P2TR(verifying_pubkey) with no script path, so only this leaf's key can
	// spend them. Renewal copies that key forward verbatim, which is why an
	// ancestor's output matches; a tree split divides it, so a sibling's never
	// does.
	expectedScript, err := common.P2TRScriptFromPubKey(leaf.VerifyingPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive the expected script for leaf %s: %w", leaf.ID, err)
	}
	if !bytes.Equal(prevOut.PkScript, expectedScript) {
		return nil, sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("outpoint %s is not spendable by leaf %s: its script is not the leaf's verifying key", outPoint, leaf.ID))
	}

	if err := validateRecoveryTxOutputs(recoveryTx, prevOut.Value); err != nil {
		return nil, err
	}

	txSighash, err := sighash.FromTx(recoveryTx, 0, prevOut)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("recovery transaction is not signable: %w", err))
	}

	return &recoverableOutput{
		sourceNodeID: sourceNode.ID,
		prevOut:      prevOut,
		sighash:      txSighash,
	}, nil
}

// findRecoverySourceNode locates the node whose direct tx created the named
// outpoint and still holds it unspent. One indexed lookup: a txid is unique.
//
// node_confirmation_height is set when *either* of a node's transactions
// confirms, so a node whose raw tx confirmed passes it too, and its direct tx can
// never confirm. Left unguarded: recovery records no outpoint, so a caller who
// names the wrong transaction of a renewal chain just calls again.
func findRecoverySourceNode(ctx context.Context, db *ent.Client, leaf *ent.TreeNode, outPoint wire.OutPoint) (*ent.TreeNode, error) {
	treeID, err := leaf.QueryTree().OnlyID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree of leaf %s: %w", leaf.ID, err)
	}

	sourceNode, err := db.TreeNode.Query().
		Where(
			enttreenode.HasTreeWith(enttree.ID(treeID)),
			enttreenode.DirectTxid(st.NewTxID(outPoint.Hash)),
			enttreenode.NodeConfirmationHeightNotNil(),
			enttreenode.NodeConfirmationHeightGT(0),
			// ON_CHAIN, not merely confirmed: a node's refund tx spends the very
			// output being recovered, and MarkExitingNodes moves the node to EXITED
			// when it does. EXITED is reachable without a refund height, so the
			// status is the gate rather than refund_confirmation_height.
			enttreenode.StatusEQ(st.TreeNodeStatusOnChain),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, sparkerrors.FailedPreconditionInvalidState(
				fmt.Errorf("outpoint %s is not a confirmed watchtower exit in the tree of leaf %s", outPoint, leaf.ID))
		}
		return nil, fmt.Errorf("failed to look up the source node for outpoint %s: %w", outPoint, err)
	}
	if len(sourceNode.DirectTx) == 0 {
		return nil, fmt.Errorf("node %s matched outpoint %s but stores no direct tx", sourceNode.ID, outPoint)
	}
	return sourceNode, nil
}

// validateRecoveryTxOutputs rejects a transaction paying out more than the
// output it spends. Where the value goes is the caller's business — the SE's
// shares are inert without theirs — but claiming more than exists is malformed.
// Uses the sentinels the other output-sum checks share.
func validateRecoveryTxOutputs(recoveryTx *wire.MsgTx, prevOutValue int64) error {
	total := int64(0)
	for _, out := range recoveryTx.TxOut {
		if out.Value < 0 {
			return sparkerrors.InvalidArgumentMalformedField(helper.ErrNegativeOutputValue)
		}
		if total > math.MaxInt64-out.Value {
			return sparkerrors.InvalidArgumentMalformedField(helper.ErrTotalOutputValueGreaterThanMaxInt64)
		}
		total += out.Value
	}
	if total > prevOutValue {
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("%w: total %d, prevout %d", helper.ErrTotalOutputValueGreaterThanPrevOutputValue, total, prevOutValue))
	}
	return nil
}
