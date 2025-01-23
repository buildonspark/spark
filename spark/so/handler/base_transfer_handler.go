package handler

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

// BaseTransferHandler is the base transfer handler that is shared for internal and external transfer handlers.
type BaseTransferHandler struct {
	config *so.Config
}

func (h *BaseTransferHandler) validateSendLeafRefundTx(leaf *ent.TreeNode, rawTx []byte, receiverIdentityKey []byte) error {
	newRefundTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return fmt.Errorf("unable to load new refund tx: %v", err)
	}
	if len(newRefundTx.TxIn) != 1 {
		return fmt.Errorf("expected 1 input in refund tx")
	}
	newRefundTxIn := newRefundTx.TxIn[0]

	oldRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("unable to load old refund tx: %v", err)
	}
	oldRefundTxIn := oldRefundTx.TxIn[0]

	if !newRefundTxIn.PreviousOutPoint.Hash.IsEqual(&oldRefundTxIn.PreviousOutPoint.Hash) || newRefundTxIn.PreviousOutPoint.Index != oldRefundTxIn.PreviousOutPoint.Index {
		return fmt.Errorf("unexpected input in new refund tx")
	}
	newTimeLock := newRefundTx.TxIn[0].Sequence & 0xFFFF
	oldTimeLock := oldRefundTx.TxIn[0].Sequence & 0xFFFF
	if newTimeLock >= oldTimeLock {
		return fmt.Errorf("time lock on the new refund tx must be less than the old one")
	}

	receiverP2trAddress, err := common.P2TRAddressFromPublicKey(receiverIdentityKey, h.config.Network)
	if err != nil {
		return fmt.Errorf("unable to generate p2tr address from receiver pubkey: %v", err)
	}
	refundP2trAddress, err := common.P2TRAddressFromPkScript(newRefundTx.TxOut[0].PkScript, h.config.Network)
	if err != nil {
		return fmt.Errorf("unable to generate p2tr address from refund pkscript: %v", err)
	}
	if *receiverP2trAddress != *refundP2trAddress {
		return fmt.Errorf("refund tx is expected to send to receiver identity pubkey")
	}

	return nil
}

func (h *BaseTransferHandler) createTransfer(ctx context.Context, transferID string, expiryTime time.Time, senderIdentityPublicKey []byte, receiverIdentityPublicKey []byte, leafRefundMap map[string][]byte) (*ent.Transfer, map[string]*ent.TreeNode, error) {
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
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	leafMap := make(map[string]*ent.TreeNode)
	totalAmount := uint64(0)

	for leafID, rawRefundTx := range leafRefundMap {
		// Find leaf in db
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to parse leaf_id %s: %v", leafID, err)
		}

		db := ent.GetDbFromContext(ctx)
		leaf, err := db.TreeNode.Get(ctx, leafUUID)
		if err != nil || leaf == nil {
			return nil, nil, fmt.Errorf("unable to find leaf %s: %v", leafID, err)
		}
		if (leaf.Status != schema.TreeNodeStatusAvailable &&
			(leaf.Status != schema.TreeNodeStatusDestinationLock || !bytes.Equal(leaf.DestinationLockIdentityPubkey, transfer.ReceiverIdentityPubkey))) ||
			!bytes.Equal(leaf.OwnerIdentityPubkey, transfer.SenderIdentityPubkey) {
			return nil, nil, fmt.Errorf("leaf %s is not available to transfer", leafID)
		}
		totalAmount += leaf.Value
		leafMap[leafID] = leaf

		err = h.validateSendLeafRefundTx(leaf, rawRefundTx, receiverIdentityPublicKey)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to validate refund tx for leaf %s: %v", leafID, err)
		}
		_, err = db.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(leaf.RawRefundTx).
			SetIntermediateRefundTx(rawRefundTx).
			Save(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to create transfer leaf: %v", err)
		}
	}
	transfer, err = db.Transfer.UpdateOne(transfer).SetTotalValue(totalAmount).Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to update transfer total value: %v", err)
	}
	return transfer, leafMap, nil
}
