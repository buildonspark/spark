package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
)

// NodeQueryHandler is the handler for query nodes
type NodeQueryHandler struct {
	config *so.Config
}

// NewNodeQueryHandler returns a new NodeQueryHandler.
func NewNodeQueryHandler(config *so.Config) *NodeQueryHandler {
	return &NodeQueryHandler{config: config}
}

func (h *NodeQueryHandler) QueryNodes(ctx context.Context, req *pb.QueryNodesRequest) (*pb.QueryNodesResponse, error) {
	nodeIDs := make([]uuid.UUID, len(req.NodeIds))
	for _, nodeID := range req.NodeIds {
		nodeUUID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id as a uuid %s: %v", nodeID, err)
		}
		nodeIDs = append(nodeIDs, nodeUUID)
	}
	db := ent.GetDbFromContext(ctx)
	nodes, err := db.TreeNode.Query().Where(treenode.IDIn(nodeIDs...)).All(ctx)
	if err != nil {
		return nil, err
	}
	nodeMap := make(map[string]*pb.TreeNode)
	for _, node := range nodes {
		nodeProto, err := node.MarshalSparkProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal node %s: %v", node.ID.String(), err)
		}
		nodeMap[node.ID.String()] = nodeProto
	}
	return &pb.QueryNodesResponse{Nodes: nodeMap}, nil
}
