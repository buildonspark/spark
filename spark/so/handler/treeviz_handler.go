//go:build lightspark

package handler

import (
	"context"
	"fmt"
	"maps"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttree "github.com/lightsparkdev/spark/so/ent/tree"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	treeVizDefaultMaxDepth     = 4
	treeVizMaxDepthCap         = 8
	treeVizDefaultMaxNodes     = 200
	treeVizMaxNodesCap         = 500
	treeVizDefaultMaxChildren  = 200
	treeVizMaxChildrenCap      = 500
	treeVizDefaultMaxTransfers = 200
	treeVizMaxTransfersCap     = 1000
)

type TreeVizHandler struct {
	config *so.Config
}

func NewTreeVizHandler(config *so.Config) *TreeVizHandler {
	return &TreeVizHandler{config: config}
}

func (h *TreeVizHandler) ResolveTreeLookup(
	ctx context.Context,
	req *pb.TreeVizResolveTreeLookupRequest,
) (*pb.TreeVizResolveTreeLookupResponse, error) {
	db, lookupID, err := h.getDbAndLookupId(ctx, req.GetLookupId())
	if err != nil {
		return nil, err
	}

	if exists, err := db.Tree.Query().Where(enttree.IDEQ(lookupID)).Exist(ctx); err != nil {
		return nil, fmt.Errorf("failed to resolve tree lookup: %w", err)
	} else if exists {
		return &pb.TreeVizResolveTreeLookupResponse{
			TreeId: lookupID.String(),
			Source: pb.TreeVizLookupSource_TREE_VIZ_LOOKUP_SOURCE_TREE,
		}, nil
	}

	node, err := db.TreeNode.Query().
		Where(enttreenode.IDEQ(lookupID)).
		WithTree().
		Only(ctx)
	if err == nil {
		if node.Edges.Tree == nil {
			return nil, status.Errorf(codes.Internal, "node %s has no associated tree", lookupID)
		}
		treeID := node.Edges.Tree.ID.String()
		return &pb.TreeVizResolveTreeLookupResponse{
			TreeId: treeID,
			Source: pb.TreeVizLookupSource_TREE_VIZ_LOOKUP_SOURCE_NODE,
		}, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to resolve node lookup: %w", err)
	}

	// All leaves of a transfer are guaranteed to belong to the same tree by
	// the transfer protocol, so any matching TransferLeaf resolves to the correct tree.
	transferLeaf, err := db.TransferLeaf.Query().
		Where(enttransferleaf.HasTransferWith(enttransfer.IDEQ(lookupID))).
		WithLeaf(func(q *ent.TreeNodeQuery) {
			q.WithTree()
		}).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "unable to resolve %s to a tree", lookupID)
		}
		return nil, fmt.Errorf("failed to resolve transfer lookup: %w", err)
	}
	if transferLeaf.Edges.Leaf == nil || transferLeaf.Edges.Leaf.Edges.Tree == nil {
		return nil, status.Errorf(codes.Internal, "transfer %s has no resolvable tree", lookupID)
	}
	treeID := transferLeaf.Edges.Leaf.Edges.Tree.ID.String()
	return &pb.TreeVizResolveTreeLookupResponse{
		TreeId: treeID,
		Source: pb.TreeVizLookupSource_TREE_VIZ_LOOKUP_SOURCE_TRANSFER,
	}, nil
}

func (h *TreeVizHandler) GetTreeSnapshot(
	ctx context.Context,
	req *pb.TreeVizGetTreeSnapshotRequest,
) (*pb.TreeVizGetTreeSnapshotResponse, error) {
	db, treeID, err := h.getDbAndLookupId(ctx, req.GetTreeId())
	if err != nil {
		return nil, err
	}

	treeEnt, err := db.Tree.Query().
		Where(enttree.IDEQ(treeID)).
		WithRoot(func(q *ent.TreeNodeQuery) {
			q.WithParent()
		}).
		WithDepositAddress().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "tree %s not found", treeID)
		}
		return nil, fmt.Errorf("failed to load tree %s: %w", treeID, err)
	}

	// The Root edge on Tree is often unset. Fall back to the node with no
	// parent, which is the root by definition in Spark's tree structure.
	if treeEnt.Edges.Root == nil {
		root, err := db.TreeNode.Query().
			Where(
				enttreenode.HasTreeWith(enttree.IDEQ(treeID)),
				enttreenode.Not(enttreenode.HasParent()),
			).
			Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, fmt.Errorf("failed to find root node for tree %s: %w", treeID, err)
		}
		if root != nil {
			treeEnt.Edges.Root = root
		}
	}

	totalNodeCount, err := db.TreeNode.Query().
		Where(enttreenode.HasTreeWith(enttree.IDEQ(treeID))).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count nodes for tree %s: %w", treeID, err)
	}

	maxDepth := clampTreeVizDepth(req.GetMaxDepth())
	maxNodes := clampTreeVizNodes(req.GetMaxNodes())

	selectedNodes, childrenByParent, err := h.loadBoundedNodes(ctx, db, treeID, treeEnt.Edges.Root, maxDepth, maxNodes)
	if err != nil {
		return nil, err
	}

	treeProto, err := marshalTreeVizTree(treeEnt)
	if err != nil {
		return nil, err
	}
	treeIDStr := treeID.String()
	nodeProtos := make([]*pb.TreeVizNode, 0, len(selectedNodes))
	for _, node := range selectedNodes {
		nodeProto, err := marshalTreeVizNode(node, treeIDStr, childrenByParent)
		if err != nil {
			return nil, err
		}
		nodeProtos = append(nodeProtos, nodeProto)
	}

	totalEdgeCount := 0
	if totalNodeCount > 0 {
		totalEdgeCount = totalNodeCount - 1
	}

	return &pb.TreeVizGetTreeSnapshotResponse{
		Tree:           treeProto,
		Nodes:          nodeProtos,
		TotalNodeCount: uint32(totalNodeCount),
		TotalEdgeCount: uint32(totalEdgeCount),
	}, nil
}

func (h *TreeVizHandler) GetNodeChildren(
	ctx context.Context,
	req *pb.TreeVizGetNodeChildrenRequest,
) (*pb.TreeVizGetNodeChildrenResponse, error) {
	db, treeID, parentID, err := h.getDbTreeAndNodeIds(ctx, req.GetTreeId(), req.GetParentNodeId())
	if err != nil {
		return nil, err
	}

	parentExists, err := db.TreeNode.Query().
		Where(
			enttreenode.IDEQ(parentID),
			enttreenode.HasTreeWith(enttree.IDEQ(treeID)),
		).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check parent node %s: %w", parentID, err)
	}
	if !parentExists {
		return nil, status.Errorf(codes.NotFound, "node %s not found in tree %s", parentID, treeID)
	}

	maxChildren := clampTreeVizChildren(req.GetMaxChildren())
	childPredicates := []predicate.TreeNode{
		enttreenode.HasTreeWith(enttree.IDEQ(treeID)),
		enttreenode.HasParentWith(enttreenode.IDEQ(parentID)),
	}
	if afterIDStr := req.GetAfterId(); afterIDStr != "" {
		afterID, err := uuid.Parse(afterIDStr)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid after_id %q", afterIDStr)
		}
		childPredicates = append(childPredicates, enttreenode.IDGT(afterID))
	}
	children, err := db.TreeNode.Query().
		Where(childPredicates...).
		WithParent().
		Order(ent.Asc(enttreenode.FieldID)).
		Limit(int(maxChildren) + 1).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load child nodes for %s: %w", parentID, err)
	}
	hasMore := len(children) > int(maxChildren)
	if hasMore {
		children = children[:maxChildren]
	}

	if len(children) == 0 {
		return &pb.TreeVizGetNodeChildrenResponse{Children: []*pb.TreeVizNode{}}, nil
	}

	childIDs := make([]uuid.UUID, len(children))
	for i, child := range children {
		childIDs[i] = child.ID
	}
	childCounts, err := countChildrenPerParent(ctx, db, treeID, childIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to count grandchildren for %s: %w", parentID, err)
	}
	treeIDStr := treeID.String()
	childProtos := make([]*pb.TreeVizNode, 0, len(children))
	for _, child := range children {
		childProto, err := marshalTreeVizNode(child, treeIDStr, childCounts)
		if err != nil {
			return nil, err
		}
		childProtos = append(childProtos, childProto)
	}
	return &pb.TreeVizGetNodeChildrenResponse{Children: childProtos, HasMore: hasMore}, nil
}

func (h *TreeVizHandler) GetTreeTransfers(
	ctx context.Context,
	req *pb.TreeVizGetTreeTransfersRequest,
) (*pb.TreeVizGetTreeTransfersResponse, error) {
	db, treeID, err := h.getDbAndLookupId(ctx, req.GetTreeId())
	if err != nil {
		return nil, err
	}

	treeExists, err := db.Tree.Query().Where(enttree.IDEQ(treeID)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check tree %s: %w", treeID, err)
	}
	if !treeExists {
		return nil, status.Errorf(codes.NotFound, "tree %s not found", treeID)
	}

	maxTransfers := clampTreeVizTransfers(req.GetMaxTransfers())
	transferLeafPredicates := []predicate.TransferLeaf{
		enttransferleaf.HasLeafWith(enttreenode.HasTreeWith(enttree.IDEQ(treeID))),
	}
	if afterIDStr := req.GetAfterId(); afterIDStr != "" {
		afterID, err := uuid.Parse(afterIDStr)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid after_id %q", afterIDStr)
		}
		transferLeafPredicates = append(transferLeafPredicates, enttransferleaf.IDGT(afterID))
	}
	transferLeafs, err := db.TransferLeaf.Query().
		Where(transferLeafPredicates...).
		WithLeaf().
		WithTransfer(func(q *ent.TransferQuery) {
			q.WithPaymentIntent().
				WithSparkInvoice().
				WithCounterSwapTransfer().
				WithPrimarySwapTransfer()
		}).
		Order(ent.Asc(enttransferleaf.FieldID)).
		Limit(int(maxTransfers) + 1).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load transfers for tree %s: %w", treeID, err)
	}

	hasMore := len(transferLeafs) > int(maxTransfers)
	if hasMore {
		transferLeafs = transferLeafs[:int(maxTransfers)]
	}

	transfers := make([]*pb.TreeVizTransfer, 0, len(transferLeafs))
	for _, transferLeaf := range transferLeafs {
		transfer, err := marshalTreeVizTransfer(transferLeaf)
		if err != nil {
			return nil, err
		}
		transfers = append(transfers, transfer)
	}
	return &pb.TreeVizGetTreeTransfersResponse{Transfers: transfers, HasMore: hasMore}, nil
}

func (h *TreeVizHandler) GetNodeDetails(
	ctx context.Context,
	req *pb.TreeVizGetNodeDetailsRequest,
) (*pb.TreeVizGetNodeDetailsResponse, error) {
	db, treeID, nodeID, err := h.getDbTreeAndNodeIds(ctx, req.GetTreeId(), req.GetNodeId())
	if err != nil {
		return nil, err
	}

	node, err := db.TreeNode.Query().
		Where(
			enttreenode.IDEQ(nodeID),
			enttreenode.HasTreeWith(enttree.IDEQ(treeID)),
		).
		WithParent().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "node %s not found in tree %s", nodeID, treeID)
		}
		return nil, fmt.Errorf("failed to load node %s: %w", nodeID, err)
	}

	childCount, err := db.TreeNode.Query().
		Where(
			enttreenode.HasTreeWith(enttree.IDEQ(treeID)),
			enttreenode.HasParentWith(enttreenode.IDEQ(nodeID)),
		).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count children for node %s: %w", nodeID, err)
	}
	childCounts := map[uuid.UUID]int{nodeID: childCount}
	nodeProto, err := marshalTreeVizNode(node, treeID.String(), childCounts)
	if err != nil {
		return nil, err
	}
	return &pb.TreeVizGetNodeDetailsResponse{
		Node: nodeProto,
	}, nil
}

func (h *TreeVizHandler) getDbAndLookupId(
	ctx context.Context,
	lookupID string,
) (*ent.Client, uuid.UUID, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	id, err := uuid.Parse(lookupID)
	if err != nil {
		return nil, uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid uuid %q", lookupID)
	}
	return db, id, nil
}

func (h *TreeVizHandler) getDbTreeAndNodeIds(
	ctx context.Context,
	treeID string,
	nodeID string,
) (*ent.Client, uuid.UUID, uuid.UUID, error) {
	db, parsedTreeID, err := h.getDbAndLookupId(ctx, treeID)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	parsedNodeID, err := uuid.Parse(nodeID)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid uuid %q", nodeID)
	}
	return db, parsedTreeID, parsedNodeID, nil
}

// loadBoundedNodes performs an iterative BFS from root, issuing one DB query
// per depth level and stopping once maxDepth or maxNodes is reached. It uses
// a separate COUNT … GROUP BY query per level so that TotalChildrenCount is
// exact for every returned node, even when the child-fetch is limited.
func (h *TreeVizHandler) loadBoundedNodes(
	ctx context.Context,
	db *ent.Client,
	treeID uuid.UUID,
	root *ent.TreeNode,
	maxDepth uint32,
	maxNodes uint32,
) ([]*ent.TreeNode, map[uuid.UUID]int, error) {
	childrenByParent := make(map[uuid.UUID]int)
	if root == nil {
		return nil, childrenByParent, nil
	}

	selected := make([]*ent.TreeNode, 0, 64)
	selected = append(selected, root)
	frontier := []uuid.UUID{root.ID}

	for depth := uint32(0); depth < maxDepth && len(frontier) > 0 && uint32(len(selected)) < maxNodes; depth++ {
		counts, err := countChildrenPerParent(ctx, db, treeID, frontier)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to count children at depth %d for tree %s: %w", depth+1, treeID, err)
		}
		maps.Copy(childrenByParent, counts)

		children, err := db.TreeNode.Query().
			Where(
				enttreenode.HasTreeWith(enttree.IDEQ(treeID)),
				enttreenode.HasParentWith(enttreenode.IDIn(frontier...)),
			).
			WithParent().
			Order(ent.Asc(enttreenode.FieldID)).
			Limit(int(maxNodes - uint32(len(selected)))).
			All(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load children at depth %d for tree %s: %w", depth+1, treeID, err)
		}

		frontier = frontier[:0]
		for _, child := range children {
			selected = append(selected, child)
			frontier = append(frontier, child.ID)
		}
	}

	// Count children of the final frontier so TotalChildrenCount is accurate
	// for nodes at the depth/size boundary.
	if len(frontier) > 0 {
		counts, err := countChildrenPerParent(ctx, db, treeID, frontier)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to count children for frontier of tree %s: %w", treeID, err)
		}
		maps.Copy(childrenByParent, counts)
	}

	return selected, childrenByParent, nil
}

// countChildrenPerParent returns the exact number of children for each node in
// parentIDs by issuing a single COUNT … GROUP BY query. It never materialises
// child rows, so the result is accurate regardless of any row-fetch limit.
func countChildrenPerParent(
	ctx context.Context,
	db *ent.Client,
	treeID uuid.UUID,
	parentIDs []uuid.UUID,
) (map[uuid.UUID]int, error) {
	// ParentID uses the same column name as enttreenode.ParentColumn ("tree_node_parent").
	// If the edge is renamed in the schema, this tag must be updated to match.
	type row struct {
		ParentID uuid.UUID `json:"tree_node_parent"`
		Count    int       `json:"count"`
	}
	var rows []row
	err := db.TreeNode.Query().
		Where(
			enttreenode.HasTreeWith(enttree.IDEQ(treeID)),
			enttreenode.HasParentWith(enttreenode.IDIn(parentIDs...)),
		).
		GroupBy(enttreenode.ParentColumn).
		Aggregate(ent.Count()).
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	counts := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		counts[r.ParentID] = r.Count
	}
	return counts, nil
}

func clampTreeVizDepth(requested uint32) uint32 {
	if requested == 0 {
		return treeVizDefaultMaxDepth
	}
	if requested > treeVizMaxDepthCap {
		return treeVizMaxDepthCap
	}
	return requested
}

func clampTreeVizNodes(requested uint32) uint32 {
	if requested == 0 {
		return treeVizDefaultMaxNodes
	}
	if requested > treeVizMaxNodesCap {
		return treeVizMaxNodesCap
	}
	return requested
}

func clampTreeVizChildren(requested uint32) uint32 {
	if requested == 0 {
		return treeVizDefaultMaxChildren
	}
	if requested > treeVizMaxChildrenCap {
		return treeVizMaxChildrenCap
	}
	return requested
}

func clampTreeVizTransfers(requested uint32) uint32 {
	if requested == 0 {
		return treeVizDefaultMaxTransfers
	}
	if requested > treeVizMaxTransfersCap {
		return treeVizMaxTransfersCap
	}
	return requested
}

func marshalTreeVizTree(treeEnt *ent.Tree) (*pb.TreeVizTreeDetails, error) {
	networkProto, err := treeEnt.Network.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tree network for %s: %w", treeEnt.ID, err)
	}
	treeRoot := ""
	if treeEnt.Edges.Root != nil {
		treeRoot = treeEnt.Edges.Root.ID.String()
	}
	depositAddress := ""
	if treeEnt.Edges.DepositAddress != nil {
		depositAddress = treeEnt.Edges.DepositAddress.Address
	}
	return &pb.TreeVizTreeDetails{
		Id:                     treeEnt.ID.String(),
		CreatedTime:            timestamppb.New(treeEnt.CreateTime),
		UpdatedTime:            timestamppb.New(treeEnt.UpdateTime),
		OwnerIdentityPublicKey: treeEnt.OwnerIdentityPubkey.Serialize(),
		Status:                 treeStatusToProto(treeEnt.Status),
		Network:                networkProto,
		BaseTxid:               txidBytesOrNil(treeEnt.BaseTxid),
		TreeRoot:               treeRoot,
		Vout:                   uint32(treeEnt.Vout),
		DepositAddressTree:     depositAddress,
	}, nil
}

func treeStatusToProto(s st.TreeStatus) pb.TreeVizTreeStatus {
	switch s {
	case st.TreeStatusPending:
		return pb.TreeVizTreeStatus_TREE_VIZ_TREE_STATUS_PENDING
	case st.TreeStatusAvailable:
		return pb.TreeVizTreeStatus_TREE_VIZ_TREE_STATUS_AVAILABLE
	case st.TreeStatusExited:
		return pb.TreeVizTreeStatus_TREE_VIZ_TREE_STATUS_EXITED
	default:
		return pb.TreeVizTreeStatus_TREE_VIZ_TREE_STATUS_UNSPECIFIED
	}
}

func nodeStatusToProto(s st.TreeNodeStatus) pb.TreeVizNodeStatus {
	switch s {
	case st.TreeNodeStatusCreating:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_CREATING
	case st.TreeNodeStatusAvailable:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_AVAILABLE
	case st.TreeNodeStatusFrozenByIssuer:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_FROZEN_BY_ISSUER
	case st.TreeNodeStatusTransferLocked:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_TRANSFER_LOCKED
	case st.TreeNodeStatusSplitLocked:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_SPLIT_LOCKED
	case st.TreeNodeStatusSplitted:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_SPLITTED
	case st.TreeNodeStatusAggregated:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_AGGREGATED
	case st.TreeNodeStatusOnChain:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_ON_CHAIN
	case st.TreeNodeStatusExited:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_EXITED
	case st.TreeNodeStatusAggregateLock:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_AGGREGATE_LOCK
	case st.TreeNodeStatusInvestigation:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_INVESTIGATION
	case st.TreeNodeStatusLost:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_LOST
	case st.TreeNodeStatusReimbursed:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_REIMBURSED
	case st.TreeNodeStatusParentExited:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_PARENT_EXITED
	case st.TreeNodeStatusRenewLocked:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_RENEW_LOCKED
	case st.TreeNodeStatusConsolidated:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_CONSOLIDATED
	default:
		return pb.TreeVizNodeStatus_TREE_VIZ_NODE_STATUS_UNSPECIFIED
	}
}

func transferStatusToProto(s st.TransferStatus) pb.TreeVizTransferStatus {
	switch s {
	case st.TransferStatusSenderInitiated:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_SENDER_INITIATED
	case st.TransferStatusSenderInitiatedCoordinator:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR
	case st.TransferStatusSenderKeyTweakPending:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING
	case st.TransferStatusApplyingSenderKeyTweak:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK
	case st.TransferStatusSenderKeyTweaked:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_SENDER_KEY_TWEAKED
	case st.TransferStatusReceiverKeyTweaked:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED
	case st.TransferStatusReceiverKeyTweakLocked:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED
	case st.TransferStatusReceiverKeyTweakApplied:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED
	case st.TransferStatusReceiverRefundSigned:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED
	case st.TransferStatusCompleted:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_COMPLETED
	case st.TransferStatusExpired:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_EXPIRED
	case st.TransferStatusReturned:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_RETURNED
	default:
		return pb.TreeVizTransferStatus_TREE_VIZ_TRANSFER_STATUS_UNSPECIFIED
	}
}

func transferTypeToProto(t st.TransferType) pb.TreeVizTransferType {
	switch t {
	case st.TransferTypePreimageSwap:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_PREIMAGE_SWAP
	case st.TransferTypeCooperativeExit:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_COOPERATIVE_EXIT
	case st.TransferTypeTransfer:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_TRANSFER
	case st.TransferTypeSwap:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_SWAP
	case st.TransferTypeCounterSwap:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_COUNTER_SWAP
	case st.TransferTypeUtxoSwap:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_UTXO_SWAP
	case st.TransferTypePrimarySwapV3:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_PRIMARY_SWAP_V3
	case st.TransferTypeCounterSwapV3:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_COUNTER_SWAP_V3
	default:
		return pb.TreeVizTransferType_TREE_VIZ_TRANSFER_TYPE_UNSPECIFIED
	}
}

func marshalTreeVizNode(
	node *ent.TreeNode,
	treeIDStr string,
	childCountByNode map[uuid.UUID]int,
) (*pb.TreeVizNode, error) {
	var parentNodeID *string
	if node.Edges.Parent != nil {
		parentNodeID = new(node.Edges.Parent.ID.String())
	}

	networkProto, err := node.Network.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal node network for %s: %w", node.ID, err)
	}
	protoNode := &pb.TreeVizNode{
		Id:                       node.ID.String(),
		TreeId:                   treeIDStr,
		ParentNodeId:             parentNodeID,
		Status:                   nodeStatusToProto(node.Status),
		Network:                  networkProto,
		Value:                    node.Value,
		Vout:                     uint32(node.Vout),
		CreatedTime:              timestamppb.New(node.CreateTime),
		UpdatedTime:              timestamppb.New(node.UpdateTime),
		VerifyingPublicKey:       node.VerifyingPubkey.Serialize(),
		OwnerIdentityPublicKey:   node.OwnerIdentityPubkey.Serialize(),
		OwnerSigningPublicKey:    node.OwnerSigningPubkey.Serialize(),
		RawTx:                    node.RawTx,
		RawRefundTx:              node.RawRefundTx,
		DirectTx:                 node.DirectTx,
		DirectRefundTx:           node.DirectRefundTx,
		DirectFromCpfpRefundTx:   node.DirectFromCpfpRefundTx,
		RawTxid:                  txidBytesOrNil(node.RawTxid),
		RawRefundTxid:            txidBytesOrNil(node.RawRefundTxid),
		DirectTxid:               txidBytesOrNil(node.DirectTxid),
		DirectRefundTxid:         txidBytesOrNil(node.DirectRefundTxid),
		DirectFromCpfpRefundTxid: txidBytesOrNil(node.DirectFromCpfpRefundTxid),
		TotalChildrenCount:       uint32(childCountByNode[node.ID]),
	}
	// The ent schema declares these fields Optional (non-Nillable uint64), so
	// 0 is the zero-value sentinel meaning "not yet confirmed on-chain".
	if node.NodeConfirmationHeight > 0 {
		protoNode.NodeConfirmationHeight = new(node.NodeConfirmationHeight)
	}
	if node.RefundConfirmationHeight > 0 {
		protoNode.RefundConfirmationHeight = new(node.RefundConfirmationHeight)
	}
	return protoNode, nil
}

func marshalTreeVizTransfer(transferLeaf *ent.TransferLeaf) (*pb.TreeVizTransfer, error) {
	transfer := transferLeaf.Edges.Transfer
	leaf := transferLeaf.Edges.Leaf
	if transfer == nil {
		return nil, fmt.Errorf("transfer_leaf %s has no associated transfer", transferLeaf.ID)
	}
	if leaf == nil {
		return nil, fmt.Errorf("transfer_leaf %s has no associated leaf", transferLeaf.ID)
	}
	networkProto, err := transfer.Network.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transfer network for %s: %w", transfer.ID, err)
	}

	paymentIntent := ""
	if transfer.Edges.PaymentIntent != nil {
		paymentIntent = transfer.Edges.PaymentIntent.PaymentIntent
	}
	sparkInvoice := ""
	if transfer.Edges.SparkInvoice != nil {
		sparkInvoice = transfer.Edges.SparkInvoice.SparkInvoice
	}
	linkedSwapTransfer := ""
	if transfer.Edges.PrimarySwapTransfer != nil {
		linkedSwapTransfer = transfer.Edges.PrimarySwapTransfer.ID.String()
	} else if len(transfer.Edges.CounterSwapTransfer) > 0 {
		linkedSwapTransfer = transfer.Edges.CounterSwapTransfer[0].ID.String()
	}

	protoTransfer := &pb.TreeVizTransfer{
		Id:                        transfer.ID.String(),
		NodeId:                    leaf.ID.String(),
		TransferLeafId:            transferLeaf.ID.String(),
		Status:                    transferStatusToProto(transfer.Status),
		Type:                      transferTypeToProto(transfer.Type),
		Network:                   networkProto,
		TotalValue:                transfer.TotalValue,
		SenderIdentityPublicKey:   transfer.SenderIdentityPubkey.Serialize(),
		ReceiverIdentityPublicKey: transfer.ReceiverIdentityPubkey.Serialize(),
		CreatedTime:               timestamppb.New(transfer.CreateTime),
		UpdatedTime:               timestamppb.New(transfer.UpdateTime),
		ExpiryTime:                timestamppb.New(transfer.ExpiryTime),
		TransferPaymentIntent:     paymentIntent,
		SparkInvoice:              sparkInvoice,
		LinkedSwapTransfer:        linkedSwapTransfer,
	}
	if transfer.CompletionTime != nil {
		protoTransfer.CompletionTime = timestamppb.New(*transfer.CompletionTime)
	}
	return protoTransfer, nil
}

func txidBytesOrNil(id st.TxID) []byte {
	if id.IsZero() {
		return nil
	}
	return id.Bytes()
}
