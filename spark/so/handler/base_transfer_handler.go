package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	enttransfer "github.com/lightsparkdev/spark-go/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark-go/so/ent/transferleaf"
	enttreenode "github.com/lightsparkdev/spark-go/so/ent/treenode"
)

// BaseTransferHandler is the base transfer handler that is shared for internal and external transfer handlers.
type BaseTransferHandler struct {
	config *so.Config
}

func validateLeafRefundTxOutput(refundTx *wire.MsgTx, receiverIdentityPublicKey []byte) error {
	if len(refundTx.TxOut) != 1 {
		return fmt.Errorf("refund tx must have exactly 1 output")
	}
	receiverIdentityPubkey, err := secp256k1.ParsePubKey(receiverIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("unable to parse receiver pubkey: %v", err)
	}
	recieverP2trScript, err := common.P2TRScriptFromPubKey(receiverIdentityPubkey)
	if err != nil {
		return fmt.Errorf("unable to generate p2tr script from receiver pubkey: %v", err)
	}
	if !bytes.Equal(recieverP2trScript, refundTx.TxOut[0].PkScript) {
		return fmt.Errorf("refund tx is expected to send to receiver identity pubkey")
	}
	return nil
}

func validateLeafRefundTxInput(refundTx *wire.MsgTx, oldSequence uint32, leafOutPoint *wire.OutPoint, expectedInputCount uint32) error {
	newTimeLock := refundTx.TxIn[0].Sequence & 0xFFFF
	oldTimeLock := oldSequence & 0xFFFF
	if newTimeLock >= oldTimeLock {
		return fmt.Errorf("time lock on the new refund tx must be less than the old one")
	}
	if len(refundTx.TxIn) != int(expectedInputCount) {
		return fmt.Errorf("refund tx should have %d inputs, but has %d", expectedInputCount, len(refundTx.TxIn))
	}
	if !refundTx.TxIn[0].PreviousOutPoint.Hash.IsEqual(&leafOutPoint.Hash) || refundTx.TxIn[0].PreviousOutPoint.Index != leafOutPoint.Index {
		return fmt.Errorf("unexpected input in refund tx")
	}
	return nil
}

func validateSendLeafRefundTx(leaf *ent.TreeNode, rawTx []byte, receiverIdentityKey []byte, expectedInputCount uint32) error {
	newRefundTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return fmt.Errorf("unable to load new refund tx: %v", err)
	}
	oldRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("unable to load old refund tx: %v", err)
	}
	oldRefundTxIn := oldRefundTx.TxIn[0]
	leafOutPoint := wire.OutPoint{
		Hash:  oldRefundTxIn.PreviousOutPoint.Hash,
		Index: oldRefundTxIn.PreviousOutPoint.Index,
	}

	err = validateLeafRefundTxInput(newRefundTx, oldRefundTxIn.Sequence, &leafOutPoint, expectedInputCount)
	if err != nil {
		return fmt.Errorf("unable to validate refund tx inputs: %v", err)
	}

	err = validateLeafRefundTxOutput(newRefundTx, receiverIdentityKey)
	if err != nil {
		return fmt.Errorf("unable to validate refund tx output: %v", err)
	}

	return nil
}

func (h *BaseTransferHandler) createTransfer(
	ctx context.Context,
	transferID string,
	transferType schema.TransferType,
	expiryTime time.Time,
	senderIdentityPublicKey []byte,
	receiverIdentityPublicKey []byte,
	leafRefundMap map[string][]byte,
	forCoopExit bool,
) (*ent.Transfer, map[string]*ent.TreeNode, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", transferID, err)
	}

	if expiryTime.Before(time.Now()) {
		return nil, nil, fmt.Errorf("invalid expiry_time %s: %v", expiryTime.String(), err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Create().
		SetID(transferUUID).
		SetSenderIdentityPubkey(senderIdentityPublicKey).
		SetReceiverIdentityPubkey(receiverIdentityPublicKey).
		SetStatus(schema.TransferStatusSenderInitiated).
		SetTotalValue(0).
		SetExpiryTime(expiryTime).
		SetType(transferType).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	leaves, err := loadLeaves(ctx, db, leafRefundMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load leaves: %v", err)
	}

	if forCoopExit {
		err = validateCooperativeExitLeaves(transfer, leaves, leafRefundMap, receiverIdentityPublicKey)
	} else {
		err = validateTransferLeaves(transfer, leaves, leafRefundMap, receiverIdentityPublicKey)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("unable to validate transfer leaves: %v", err)
	}

	err = createTransferLeaves(ctx, db, transfer, leaves, leafRefundMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer leaves: %v", err)
	}

	err = setTotalTransferValue(ctx, db, transfer, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to update transfer total value: %v", err)
	}

	leafMap := make(map[string]*ent.TreeNode)
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	return transfer, leafMap, nil
}

func loadLeaves(ctx context.Context, db *ent.Tx, leafRefundMap map[string][]byte) ([]*ent.TreeNode, error) {
	leaves := make([]*ent.TreeNode, 0)
	for leafID := range leafRefundMap {
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf_id %s: %v", leafID, err)
		}

		leaf, err := db.TreeNode.Get(ctx, leafUUID)
		if err != nil || leaf == nil {
			return nil, fmt.Errorf("unable to find leaf %s: %v", leafID, err)
		}
		leaves = append(leaves, leaf)
	}
	return leaves, nil
}

func validateCooperativeExitLeaves(transfer *ent.Transfer, leaves []*ent.TreeNode, leafRefundMap map[string][]byte, receiverIdentityPublicKey []byte) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		err := validateSendLeafRefundTx(leaf, rawRefundTx, receiverIdentityPublicKey, 2)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %v", leaf.ID, err)
		}
		err = leafAvailableToTransfer(leaf, transfer.SenderIdentityPubkey, transfer.ReceiverIdentityPubkey)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %v", leaf.ID, err)
		}
	}
	return nil
}

func validateTransferLeaves(transfer *ent.Transfer, leaves []*ent.TreeNode, leafRefundMap map[string][]byte, receiverIdentityPublicKey []byte) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		err := validateSendLeafRefundTx(leaf, rawRefundTx, receiverIdentityPublicKey, 1)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %v", leaf.ID, err)
		}
		err = leafAvailableToTransfer(leaf, transfer.SenderIdentityPubkey, transfer.ReceiverIdentityPubkey)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %v", leaf.ID, err)
		}
	}
	return nil
}

func leafAvailableToTransfer(leaf *ent.TreeNode, senderIdentityPublicKey []byte, receiverIdentityPubkey []byte) error {
	if leaf.Status != schema.TreeNodeStatusAvailable &&
		(leaf.Status != schema.TreeNodeStatusDestinationLock || !bytes.Equal(leaf.DestinationLockIdentityPubkey, receiverIdentityPubkey)) {
		return fmt.Errorf("leaf %s is not available to transfer", leaf.ID.String())
	}
	if !bytes.Equal(leaf.OwnerIdentityPubkey, senderIdentityPublicKey) {
		return fmt.Errorf("leaf %s is not owned by sender", leaf.ID.String())
	}
	return nil
}

func createTransferLeaves(ctx context.Context, db *ent.Tx, transfer *ent.Transfer, leaves []*ent.TreeNode, leafRefundMap map[string][]byte) error {
	log.Println("Transferid:", transfer.ID.String())
	for _, leaf := range leaves {
		log.Println("creating leaf id:", leaf.ID.String())
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		leafEnt, err := db.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(leaf.RawRefundTx).
			SetIntermediateRefundTx(rawRefundTx).
			Save(ctx)
		log.Println("created leafEnt id:", leafEnt.ID.String())
		if err != nil {
			return fmt.Errorf("unable to create transfer leaf: %v", err)
		}

		_, err = db.TransferLeaf.
			Query().
			Where(
				enttransferleaf.HasTransferWith(enttransfer.IDEQ(transfer.ID)),
				enttransferleaf.HasLeafWith(enttreenode.IDEQ(leaf.ID)),
			).
			Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to find transfer leaf right after writing: %v", err)
		}
	}
	return nil
}

func setTotalTransferValue(ctx context.Context, db *ent.Tx, transfer *ent.Transfer, leaves []*ent.TreeNode) error {
	totalAmount := uint64(0)
	for _, leaf := range leaves {
		totalAmount += leaf.Value
	}
	_, err := db.Transfer.UpdateOne(transfer).SetTotalValue(totalAmount).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer total value: %v", err)
	}
	return nil
}
