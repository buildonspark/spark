package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
)

// InternalRefreshTimelockHandler is the refresh timelock handler for so internal.
type InternalRefreshTimelockHandler struct {
	config *so.Config
}

// NewInternalRefreshTimelockHandler creates a new InternalRefreshTimelockHandler.
func NewInternalRefreshTimelockHandler(config *so.Config) *InternalRefreshTimelockHandler {
	return &InternalRefreshTimelockHandler{
		config: config,
	}
}

// FinalizeRefreshTimelock finalizes a refresh timelock.
// Just save the new txs in the DB.
func (h *InternalRefreshTimelockHandler) FinalizeRefreshTimelock(ctx context.Context, req *pbinternal.FinalizeRefreshTimelockRequest) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	for _, node := range req.Nodes {
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
			SetDirectTx(node.DirectTx).
			SetDirectRefundTx(node.DirectRefundTx).
			SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx).
			Save(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}
