package entutils

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

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
