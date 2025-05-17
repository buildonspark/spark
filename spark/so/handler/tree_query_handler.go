package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
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
	limit := int(req.GetLimit())
	offset := int(req.GetOffset())

	switch req.Source.(type) {
	case *pb.QueryNodesRequest_OwnerIdentityPubkey:
		if limit < 0 || offset < 0 {
			return nil, fmt.Errorf("expect non-negative offset and limit")
		}
		query = query.
			Where(treenode.StatusNotIn(schema.TreeNodeStatusCreating, schema.TreeNodeStatusSplitted)).
			Where(treenode.OwnerIdentityPubkey(req.GetOwnerIdentityPubkey())).
			Order(ent.Desc(enttreenode.FieldUpdateTime))

		if limit > 0 {
			if limit > 100 {
				limit = 100
			}
			query = query.Offset(offset).Limit(limit)
		} else {
			offset = -1
		}

	case *pb.QueryNodesRequest_NodeIds:
		offset = -1

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

	response := &pb.QueryNodesResponse{
		Nodes: protoNodeMap,
	}
	if offset != -1 {
		nextOffset := -1
		if len(nodes) == limit {
			nextOffset = offset + len(nodes)
		}
		response.Offset = int64(nextOffset)
	}
	return response, nil
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
		// Exclude static deposit addresses, because they always can be used,
		// whereas express deposit addresses can be used only once
		Where(depositaddress.IsStatic(false)).
		Order(ent.Desc(depositaddress.FieldID)).
		WithSigningKeyshare()

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
			nodeIDStr := depositAddress.NodeID.String()
			unusedDepositAddresses = append(unusedDepositAddresses, &pb.DepositAddressQueryResult{
				DepositAddress:       depositAddress.Address,
				UserSigningPublicKey: depositAddress.OwnerSigningPubkey,
				VerifyingPublicKey:   verifyingPublicKey,
				LeafId:               &nodeIDStr,
			})
		}
	}

	return &pb.QueryUnusedDepositAddressesResponse{
		DepositAddresses: unusedDepositAddresses,
	}, nil
}
