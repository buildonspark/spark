package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

// InternalTransferHandler is the transfer handler for so internal
type InternalTransferHandler struct {
	config *so.Config
}

// NewInternalTransferHandler creates a new InternalTransferHandler.
func NewInternalTransferHandler(config *so.Config) *InternalTransferHandler {
	return &InternalTransferHandler{config: config}
}

// FinalizeTransfer finalizes a transfer.
func (h *InternalTransferHandler) FinalizeTransfer(ctx context.Context, req *pbinternal.FinalizeTransferRequest) error {
	db := ent.GetDbFromContext(ctx)
	transferID, err := uuid.Parse(req.TransferId)
	if err != nil {
		return err
	}
	transfer, err := db.Transfer.Get(ctx, transferID)
	if err != nil {
		return err
	}
	if transfer.Status != schema.TransferStatusKeyTweaked {
		return fmt.Errorf("transfer is not in key tweaked status")
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
		node, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return err
		}
		_, err = node.Update().
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
