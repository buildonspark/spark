package entutils

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
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
