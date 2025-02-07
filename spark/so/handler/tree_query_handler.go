package handler

import (
	"context"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
)

// TreeQueryHandler handles queries related to tree nodes.
type TreeQueryHandler struct {
	config *so.Config
}

// NewTreeQueryHandler creates a new TreeQueryHandler.
func NewTreeQueryHandler(config *so.Config) *TreeQueryHandler {
	return &TreeQueryHandler{config: config}
}

// GetTreeNodesByPublicKey returns all nodes owned by a public key and their related nodes in a flat structure
func (h *TreeQueryHandler) GetTreeNodesByPublicKey(ctx context.Context, req *pb.TreeNodesByPublicKeyRequest) (*pb.TreeNodesByPublicKeyResponse, error) {
	db := ent.GetDbFromContext(ctx)

	// First, get all nodes owned by the given public key
	ownedNodes, err := db.TreeNode.Query().
		Where(treenode.OwnerIdentityPubkey(req.OwnerIdentityPubkey)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// Create a map to track unique nodes we've seen
	protoNodeMap := make(map[string]*pb.TreeNode)
	var orderedNodes []*pb.TreeNode

	// Process each owned node and its ancestors in pre-order
	for _, node := range ownedNodes {
		err := getAncestorChainPreOrder(ctx, db, node, protoNodeMap, &orderedNodes)
		if err != nil {
			return nil, err
		}
	}

	return &pb.TreeNodesByPublicKeyResponse{
		Nodes: orderedNodes,
	}, nil
}

// Helper function to process node and ancestors in pre-order
func getAncestorChainPreOrder(ctx context.Context, db *ent.Tx, node *ent.TreeNode, nodeMap map[string]*pb.TreeNode, orderedNodes *[]*pb.TreeNode) error {
	// Skip if already processed
	if _, exists := nodeMap[node.ID.String()]; exists {
		return nil
	}

	// Use MarshalSparkProto instead of manual construction
	protoNode, err := node.MarshalSparkProto(ctx)
	if err != nil {
		return err
	}

	// Get parent node and continue chain if parent exists
	parent, err := node.QueryParent().Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		// No parent (root node), just add current node
		nodeMap[node.ID.String()] = protoNode
		*orderedNodes = append(*orderedNodes, protoNode)
		return nil
	}

	// Parent exists, continue chain
	nodeMap[node.ID.String()] = protoNode
	*orderedNodes = append(*orderedNodes, protoNode)

	return getAncestorChainPreOrder(ctx, db, parent, nodeMap, orderedNodes)
}
