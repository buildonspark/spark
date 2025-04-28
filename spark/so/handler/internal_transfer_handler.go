package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
)

// InternalTransferHandler is the transfer handler for so internal
type InternalTransferHandler struct {
	BaseTransferHandler
	config *so.Config
}

// NewInternalTransferHandler creates a new InternalTransferHandler.
func NewInternalTransferHandler(config *so.Config) *InternalTransferHandler {
	return &InternalTransferHandler{BaseTransferHandler: NewBaseTransferHandler(config), config: config}
}

// FinalizeTransfer finalizes a transfer.
func (h *InternalTransferHandler) FinalizeTransfer(ctx context.Context, req *pbinternal.FinalizeTransferRequest) error {
	db := ent.GetDbFromContext(ctx)
	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %v", req.TransferId, err)
	}

	if transfer.Status != schema.TransferStatusReceiverKeyTweaked && transfer.Status != schema.TransferStatusReceiverKeyTweakLocked && transfer.Status != schema.TransferStatusReceiverRefundSigned {
		return fmt.Errorf("transfer is not in receiver key tweaked status")
	}
	if err := checkCoopExitTxBroadcasted(ctx, db, transfer); err != nil {
		return fmt.Errorf("failed to unlock transfer %s: %v", req.TransferId, err)
	}

	transferNodes, err := transfer.QueryTransferLeaves().QueryLeaf().All(ctx)
	if err != nil {
		return err
	}
	if len(transferNodes) != len(req.Nodes) {
		return fmt.Errorf("transfer nodes count mismatch")
	}
	transferNodeIDs := make(map[string]string)
	for _, node := range transferNodes {
		transferNodeIDs[node.ID.String()] = node.ID.String()
	}

	for _, node := range req.Nodes {
		if _, ok := transferNodeIDs[node.Id]; !ok {
			return fmt.Errorf("node not found in transfer")
		}

		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return err
		}
		dbNode, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return err
		}
		_, err = dbNode.Update().
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx).
			SetStatus(schema.TreeNodeStatusAvailable).
			Save(ctx)
		if err != nil {
			return err
		}
	}

	_, err = transfer.Update().SetStatus(schema.TransferStatusCompleted).SetCompletionTime(req.Timestamp.AsTime()).Save(ctx)
	if err != nil {
		return err
	}
	return nil
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (h *InternalTransferHandler) InitiateTransfer(ctx context.Context, req *pbinternal.InitiateTransferRequest) error {
	leafRefundMap := make(map[string][]byte)
	for _, leaf := range req.Leaves {
		leafRefundMap[leaf.LeafId] = leaf.RawRefundTx
	}
	_, _, err := h.createTransfer(
		ctx,
		req.TransferId,
		schema.TransferTypeTransfer,
		req.ExpiryTime.AsTime(),
		req.SenderIdentityPublicKey,
		req.ReceiverIdentityPublicKey,
		leafRefundMap,
		req.SenderKeyTweakProofs,
	)
	return err
}

// InitiateCooperativeExit initiates a cooperative exit by creating transfer and transfer_leaf,
// and saving the exit txid.
func (h *InternalTransferHandler) InitiateCooperativeExit(ctx context.Context, req *pbinternal.InitiateCooperativeExitRequest) error {
	transferReq := req.Transfer
	leafRefundMap := make(map[string][]byte)
	for _, leaf := range transferReq.Leaves {
		leafRefundMap[leaf.LeafId] = leaf.RawRefundTx
	}
	transfer, _, err := h.createTransfer(
		ctx,
		transferReq.TransferId,
		schema.TransferTypeCooperativeExit,
		transferReq.ExpiryTime.AsTime(),
		transferReq.SenderIdentityPublicKey,
		transferReq.ReceiverIdentityPublicKey,
		leafRefundMap,
		transferReq.SenderKeyTweakProofs,
	)
	if err != nil {
		return err
	}

	exitID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return err
	}

	db := ent.GetDbFromContext(ctx)
	_, err = db.CooperativeExit.Create().
		SetID(exitID).
		SetTransfer(transfer).
		SetExitTxid(req.ExitTxid).
		Save(ctx)
	if err != nil {
		return err
	}
	return err
}
