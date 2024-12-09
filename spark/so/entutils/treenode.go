package entutils

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

// GetNodeKeyshare returns the signing keyshare for the given node ID.
func GetNodeKeyshare(ctx context.Context, config *so.Config, nodeID uuid.UUID) (*ent.SigningKeyshare, error) {
	db := common.GetDbFromContext(ctx)

	node, err := db.TreeNode.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	keyshare, err := db.SigningKeyshare.Get(ctx, node.Edges.SigningKeyshare.ID)
	if err != nil {
		return nil, err
	}
	return keyshare, nil
}

// MarkNodeAsLocked marks the node as locked.
// It will only update the node status if it is in a state to be locked.
func MarkNodeAsLocked(ctx context.Context, nodeID uuid.UUID, nodeStatus schema.TreeNodeStatus) error {
	db := common.GetDbFromContext(ctx)
	if nodeStatus != schema.TreeNodeStatusSplitLocked && nodeStatus != schema.TreeNodeStatusTransferLocked {
		return fmt.Errorf("Not updating node status to a locked state: %s", nodeStatus)
	}

	node, err := db.TreeNode.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if node.Status != schema.TreeNodeStatusAvailable {
		return fmt.Errorf("Node not in a state to be locked: %s", node.Status)
	}

	return db.TreeNode.UpdateOneID(nodeID).SetStatus(nodeStatus).Exec(ctx)
}
