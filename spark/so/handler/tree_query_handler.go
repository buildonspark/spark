package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
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

// QueryNodes queries the details of nodes given either the owner identity public key or a list of node ids.
func (h *TreeQueryHandler) QueryNodes(ctx context.Context, req *pb.QueryNodesRequest) (*pb.QueryNodesResponse, error) {
	db := ent.GetDbFromContext(ctx)

	query := db.TreeNode.Query()
	switch req.Source.(type) {
	case *pb.QueryNodesRequest_OwnerIdentityPubkey:
		query = query.
			Where(treenode.StatusNotIn(schema.TreeNodeStatusCreating, schema.TreeNodeStatusSplitted)).
			Where(treenode.OwnerIdentityPubkey(req.GetOwnerIdentityPubkey()))
	case *pb.QueryNodesRequest_NodeIds:
		nodeIDs := make([]uuid.UUID, len(req.GetNodeIds().NodeIds))
		for _, nodeID := range req.GetNodeIds().NodeIds {
			nodeUUID, err := uuid.Parse(nodeID)
			if err != nil {
				return nil, fmt.Errorf("unable to parse node id as a uuid %s: %v", nodeID, err)
			}
			nodeIDs = append(nodeIDs, nodeUUID)
		}
		query = query.Where(treenode.IDIn(nodeIDs...))
	}

	nodes, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	protoNodeMap := make(map[string]*pb.TreeNode)
	for _, node := range nodes {
		protoNodeMap[node.ID.String()], err = node.MarshalSparkProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal node %s: %v", node.ID.String(), err)
		}
		if req.IncludeParents {
			err := getAncestorChain(ctx, db, node, protoNodeMap)
			if err != nil {
				return nil, err
			}
		}
	}

	return &pb.QueryNodesResponse{
		Nodes: protoNodeMap,
	}, nil
}

func (h *TreeQueryHandler) QueryBalance(ctx context.Context, req *pb.QueryBalanceRequest) (*pb.QueryBalanceResponse, error) {
	db := ent.GetDbFromContext(ctx)

	query := db.TreeNode.Query()
	query = query.Where(treenode.StatusEQ(schema.TreeNodeStatusAvailable)).
		Where(treenode.OwnerIdentityPubkey(req.GetIdentityPublicKey()))

	nodes, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	balance := uint64(0)
	nodeBalances := make(map[string]uint64)
	for _, node := range nodes {
		balance += node.Value
		nodeBalances[node.ID.String()] = node.Value
	}

	return &pb.QueryBalanceResponse{
		Balance:      balance,
		NodeBalances: nodeBalances,
	}, nil
}

func getAncestorChain(ctx context.Context, db *ent.Tx, node *ent.TreeNode, nodeMap map[string]*pb.TreeNode) error {
	parent, err := node.QueryParent().Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		return nil
	}

	// Parent exists, continue search
	nodeMap[parent.ID.String()], err = parent.MarshalSparkProto(ctx)
	if err != nil {
		return fmt.Errorf("unable to marshal node %s: %v", parent.ID.String(), err)
	}

	return getAncestorChain(ctx, db, parent, nodeMap)
}

func (h *TreeQueryHandler) QueryUnusedDepositAddresses(ctx context.Context, req *pb.QueryUnusedDepositAddressesRequest) (*pb.QueryUnusedDepositAddressesResponse, error) {
	db := ent.GetDbFromContext(ctx)

	query := db.DepositAddress.Query()
	query = query.
		Where(depositaddress.OwnerIdentityPubkey(req.GetIdentityPublicKey())).
		Order(ent.Desc(depositaddress.FieldID)).
		WithSigningKeyshare().
		Limit(10)

	depositAddresses, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	unusedDepositAddresses := make([]*pb.DepositAddressQueryResult, 0)
	for _, depositAddress := range depositAddresses {
		treeNodes, err := db.TreeNode.Query().Where(treenode.HasSigningKeyshareWith(signingkeyshare.ID(depositAddress.Edges.SigningKeyshare.ID))).All(ctx)
		if len(treeNodes) == 0 || ent.IsNotFound(err) {
			verifyingPublicKey, err := common.AddPublicKeys(depositAddress.OwnerSigningPubkey, depositAddress.Edges.SigningKeyshare.PublicKey)
			if err != nil {
				return nil, err
			}
			unusedDepositAddresses = append(unusedDepositAddresses, &pb.DepositAddressQueryResult{
				DepositAddress:       depositAddress.Address,
				UserSigningPublicKey: depositAddress.OwnerSigningPubkey,
				VerifyingPublicKey:   verifyingPublicKey,
			})
		}
	}

	return &pb.QueryUnusedDepositAddressesResponse{
		DepositAddresses: unusedDepositAddresses,
	}, nil
}
