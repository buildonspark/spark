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
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/utils"
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
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	query := db.TreeNode.Query()
	limit := int(req.GetLimit())
	offset := int(req.GetOffset())

	var network st.Network
	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		network = st.NetworkMainnet
	} else {
		var err error
		network, err = common.SchemaNetworkFromProtoNetwork(req.GetNetwork())
		if err != nil {
			return nil, fmt.Errorf("failed to convert proto network to schema network: %w", err)
		}
	}

	switch req.Source.(type) {
	case *pb.QueryNodesRequest_OwnerIdentityPubkey:
		if limit < 0 || offset < 0 {
			return nil, fmt.Errorf("expect non-negative offset and limit")
		}
		query = query.
			Where(treenode.StatusNotIn(st.TreeNodeStatusCreating, st.TreeNodeStatusSplitted, st.TreeNodeStatusInvestigation, st.TreeNodeStatusLost, st.TreeNodeStatusReimbursed)).
			Where(treenode.HasTreeWith(
				tree.NetworkEQ(network),
			)).
			Where(treenode.OwnerIdentityPubkey(req.GetOwnerIdentityPubkey())).
			Order(ent.Desc(enttreenode.FieldID))

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
				return nil, fmt.Errorf("unable to parse node id as a uuid %s: %w", nodeID, err)
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
			return nil, fmt.Errorf("unable to marshal node %s: %w", node.ID.String(), err)
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
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var network st.Network
	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		network = st.NetworkMainnet
	} else {
		var err error
		network, err = common.SchemaNetworkFromProtoNetwork(req.GetNetwork())
		if err != nil {
			return nil, fmt.Errorf("failed to convert proto network to schema network: %w", err)
		}
	}

	query := db.TreeNode.Query()
	query = query.
		Where(treenode.HasTreeWith(
			tree.NetworkEQ(network),
		)).
		Where(treenode.StatusEQ(st.TreeNodeStatusAvailable)).
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

	// skip root node to temporarily disable unilateral exit.
	_, err = parent.QueryParent().Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		tree, err := node.QueryTree().Only(ctx)
		if err != nil {
			return err
		}
		if tree.Network == st.NetworkMainnet {
			return nil
		}
	}

	// Parent exists, continue search
	nodeMap[parent.ID.String()], err = parent.MarshalSparkProto(ctx)
	if err != nil {
		return fmt.Errorf("unable to marshal node %s: %w", parent.ID.String(), err)
	}

	return getAncestorChain(ctx, db, parent, nodeMap)
}

func (h *TreeQueryHandler) QueryUnusedDepositAddresses(ctx context.Context, req *pb.QueryUnusedDepositAddressesRequest) (*pb.QueryUnusedDepositAddressesResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	query := db.DepositAddress.Query()
	query = query.
		Where(depositaddress.OwnerIdentityPubkey(req.GetIdentityPublicKey())).
		// Exclude static deposit addresses, because they always can be used,
		// whereas express deposit addresses can be used only once
		Where(depositaddress.IsStatic(false)).
		Order(ent.Desc(depositaddress.FieldID)).
		WithSigningKeyshare()

	// Validate offset and limit
	if req.Limit < 0 || req.Offset < 0 {
		return nil, fmt.Errorf("expect non-negative offset and limit")
	}

	usePagination := req.Limit > 0 || req.Offset > 0
	limit := 100
	offset := int(req.Offset)

	// If limit and offset are provided, update query to include them otherwise don't add limit and offset to maintain backwards compatibility
	if usePagination {
		if req.Limit > 0 && req.Limit < 100 {
			limit = int(req.Limit)
		}

		query = query.Offset(offset).Limit(limit)
	}

	depositAddresses, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	var network common.Network
	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		network = common.Mainnet
	} else {
		var err error
		network, err = common.NetworkFromProtoNetwork(req.GetNetwork())
		if err != nil {
			return nil, fmt.Errorf("failed to convert proto network to common network: %w", err)
		}
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
			if utils.IsBitcoinAddressForNetwork(depositAddress.Address, network) {
				unusedDepositAddresses = append(unusedDepositAddresses, &pb.DepositAddressQueryResult{
					DepositAddress:       depositAddress.Address,
					UserSigningPublicKey: depositAddress.OwnerSigningPubkey,
					VerifyingPublicKey:   verifyingPublicKey,
					LeafId:               &nodeIDStr,
				})
			}
		}
	}

	nextOffset := -1
	if usePagination && len(unusedDepositAddresses) == limit {
		nextOffset = offset + limit
	}

	return &pb.QueryUnusedDepositAddressesResponse{
		DepositAddresses: unusedDepositAddresses,
		Offset:           int64(nextOffset),
	}, nil
}

func (h *TreeQueryHandler) QueryStaticDepositAddresses(ctx context.Context, req *pb.QueryStaticDepositAddressesRequest) (*pb.QueryStaticDepositAddressesResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	limit := int(req.GetLimit())
	offset := int(req.GetOffset())
	if limit < 0 || offset < 0 {
		return nil, fmt.Errorf("expect non-negative offset and limit")
	}
	if limit > 100 {
		limit = 100
	} else if limit == 0 {
		limit = 100
	}

	query := db.DepositAddress.Query()
	query = query.
		Where(depositaddress.OwnerIdentityPubkey(req.GetIdentityPublicKey())).
		Where(depositaddress.IsStatic(true)).
		Order(ent.Desc(depositaddress.FieldID)).
		WithSigningKeyshare().
		Offset(offset).
		Limit(limit)
	if req.DepositAddress != nil {
		query = query.Where(depositaddress.Address(req.GetDepositAddress()))
	}
	depositAddresses, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	var network common.Network
	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		network = common.Mainnet
	} else {
		var err error
		network, err = common.NetworkFromProtoNetwork(req.GetNetwork())
		if err != nil {
			return nil, fmt.Errorf("failed to convert proto network to common network: %w", err)
		}
	}

	staticDepositAddresses := make([]*pb.DepositAddressQueryResult, 0)
	for _, depositAddress := range depositAddresses {
		if utils.IsBitcoinAddressForNetwork(depositAddress.Address, network) {
			queryResult, err := h.depositAddressToQueryResult(ctx, depositAddress)
			if err != nil {
				return nil, err
			}
			// If the query result is nil, it means that the proofs of possession can not be obtained for some SOs.
			if queryResult != nil {
				staticDepositAddresses = append(staticDepositAddresses, queryResult)
			}
		}
	}

	return &pb.QueryStaticDepositAddressesResponse{
		DepositAddresses: staticDepositAddresses,
	}, nil
}

func (h *TreeQueryHandler) depositAddressToQueryResult(ctx context.Context, depositAddress *ent.DepositAddress) (*pb.DepositAddressQueryResult, error) {
	nodeIDStr := depositAddress.NodeID.String()
	verifyingPublicKey, err := common.AddPublicKeys(depositAddress.OwnerSigningPubkey, depositAddress.Edges.SigningKeyshare.PublicKey)
	if err != nil {
		return nil, err
	}

	// Get local keyshare for the deposit address.
	keyshare, err := depositAddress.Edges.SigningKeyshareOrErr()
	if err != nil {
		return nil, fmt.Errorf("failed to get keyshare for static deposit address: %w", err)
	}

	// Return the proofs of possession if they are cached.
	// Caching is done in the GenerateStaticDepositAddressResponse handler on the coordinator.
	// If there are no proofs of possession, the user is advised to generate them by calling the GenerateStaticDepositAddressProofs RPC.
	var proofOfPossession *pb.DepositAddressProof
	if depositAddress.AddressSignatures != nil && depositAddress.PossessionSignature != nil {
		proofOfPossession = &pb.DepositAddressProof{
			AddressSignatures:          depositAddress.AddressSignatures,
			ProofOfPossessionSignature: depositAddress.PossessionSignature,
		}
	} else {
		addressSignatures, proofOfPossessionSignature, err := generateStaticDepositAddressProofs(ctx, h.config, keyshare, depositAddress)
		if err != nil {
			return nil, err
		}
		if addressSignatures == nil {
			return nil, nil
		}

		proofOfPossession = &pb.DepositAddressProof{
			AddressSignatures:          addressSignatures,
			ProofOfPossessionSignature: proofOfPossessionSignature,
		}
	}

	return &pb.DepositAddressQueryResult{
		DepositAddress:       depositAddress.Address,
		UserSigningPublicKey: depositAddress.OwnerSigningPubkey,
		VerifyingPublicKey:   verifyingPublicKey,
		LeafId:               &nodeIDStr,
		ProofOfPossession:    proofOfPossession,
	}, nil
}

func (h *TreeQueryHandler) QueryNodesDistribution(ctx context.Context, req *pb.QueryNodesDistributionRequest) (*pb.QueryNodesDistributionResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	type Result struct {
		Value uint64 `json:"value"`
		Count int    `json:"count"`
	}

	var results []Result

	err = db.TreeNode.Query().
		Where(
			treenode.OwnerIdentityPubkey(req.GetOwnerIdentityPublicKey()),
			treenode.StatusEQ(st.TreeNodeStatusAvailable),
		).
		GroupBy(treenode.FieldValue).
		Aggregate(ent.Count()).
		Scan(ctx, &results)
	if err != nil {
		return nil, fmt.Errorf("failed to query tree nodes: %w", err)
	}

	resultMap := make(map[uint64]uint64)
	for _, result := range results {
		resultMap[result.Value] = uint64(result.Count)
	}

	return &pb.QueryNodesDistributionResponse{
		NodeDistribution: resultMap,
	}, nil
}

func (h *TreeQueryHandler) QueryNodesByValue(ctx context.Context, req *pb.QueryNodesByValueRequest) (*pb.QueryNodesByValueResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	limit := int(req.GetLimit())
	offset := int(req.GetOffset())

	if limit < 0 || offset < 0 {
		return nil, fmt.Errorf("expect non-negative offset and limit")
	}

	query := db.TreeNode.Query()
	query = query.
		Where(treenode.OwnerIdentityPubkey(req.GetOwnerIdentityPublicKey())).
		Where(treenode.StatusEQ(st.TreeNodeStatusAvailable)).
		Where(treenode.ValueEQ(uint64(req.GetValue()))).
		Order(ent.Desc(treenode.FieldID))

	if limit > 100 {
		limit = 100
	} else if limit == 0 {
		limit = 100
	}
	query = query.Offset(offset).Limit(limit)

	nodes, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query tree nodes: %w", err)
	}

	protoNodeMap := make(map[string]*pb.TreeNode)
	for _, node := range nodes {
		protoNodeMap[node.ID.String()], err = node.MarshalSparkProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal node %s: %w", node.ID.String(), err)
		}
	}

	response := &pb.QueryNodesByValueResponse{
		Nodes: protoNodeMap,
	}

	nextOffset := -1
	if len(nodes) == limit {
		nextOffset = offset + len(nodes)
	}
	response.Offset = int64(nextOffset)

	return response, nil
}
