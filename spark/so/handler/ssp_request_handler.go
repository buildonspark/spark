package handler

import (
	"context"
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
)

type SspRequestHandler struct {
	config *so.Config
}

func NewSspRequestHandler(config *so.Config) *SspRequestHandler {
	return &SspRequestHandler{config: config}
}

func (h *SspRequestHandler) QueryLostNodes(ctx context.Context, req *pbssp.QueryLostNodesRequest) (*pbssp.QueryLostNodesResponse, error) {
	// TOOD yunyu: allow only ssp
	db := ent.GetDbFromContext(ctx)

	query := db.TreeNode.Query().Where(enttreenode.StatusEQ(schema.TreeNodeStatusLost))
	if req.GetOwnerIdentityPubkey() != nil {
		query = query.Where(enttreenode.OwnerIdentityPubkeyEQ(req.OwnerIdentityPubkey))
	}
	nodes, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	protoNodes := make([]*pb.TreeNode, 0)
	for _, node := range nodes {
		protoNode, err := node.MarshalSparkProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal node %s: %w", node.ID, err)
		}
		protoNodes = append(protoNodes, protoNode)
	}

	return &pbssp.QueryLostNodesResponse{
		Nodes: protoNodes,
	}, nil
}
