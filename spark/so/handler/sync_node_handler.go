package handler

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uuids"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbin "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"
)

type SyncNodeHandler struct {
	config *so.Config
}

func NewSyncNodeHandler(soConfig *so.Config) SyncNodeHandler {
	return SyncNodeHandler{
		config: soConfig,
	}
}

func (h *SyncNodeHandler) SyncTreeNodes(ctx context.Context, req *pbin.SyncNodeRequest) error {
	if len(req.NodeIds) == 0 || len(req.NodeIds) > 100 {
		return fmt.Errorf("invalid node ids: %v", req.NodeIds)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodeUUIDsToFix, err := uuids.ParseSlice(req.GetNodeIds())
	if err != nil {
		return fmt.Errorf("unable to parse node id: %w", err)
	}
	localNodes, err := db.TreeNode.Query().
		Where(treenode.IDIn(nodeUUIDsToFix...)).
		WithParent().
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to lock tree nodes %v: %w", nodeUUIDsToFix, err)
	}

	conn, err := h.config.SigningOperatorMap[req.OperatorId].NewOperatorGRPCConnection()
	if err != nil {
		return fmt.Errorf("failed to get operator grpc connection: %w", err)
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)
	resp, err := client.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: req.NodeIds,
			},
		},
		IncludeParents: false,
	})
	if err != nil {
		return fmt.Errorf("failed to query nodes: %w", err)
	}

	if len(resp.Nodes) != len(req.NodeIds) {
		return fmt.Errorf("expected %d nodes, got %d", len(req.NodeIds), len(resp.Nodes))
	}

	goodNodeIDMap := make(map[string]*pb.TreeNode)
	for _, node := range resp.Nodes {
		goodNodeIDMap[node.Id] = node
	}

	// Create a map of existing node UUIDs for quick lookup
	existingNodeMap := make(map[uuid.UUID]*ent.TreeNode)
	for _, node := range localNodes {
		existingNodeMap[node.ID] = node
	}

	// Phase 1: Create missing split nodes first
	// This ensures parent nodes exist before we try to update references to them
	for _, nodeUUID := range nodeUUIDsToFix {
		node, ok := goodNodeIDMap[nodeUUID.String()]
		if !ok {
			return fmt.Errorf("node %s not found in response", nodeUUID)
		}

		_, exists := existingNodeMap[nodeUUID]
		if !exists {
			// Validate status before creating
			if node.Status != "SPLITTED" && node.Status != "SPLIT_LOCKED" {
				return fmt.Errorf("cannot create node %s with status %s: only SPLITTED or SPLIT_LOCKED nodes can be created during sync", node.Id, node.Status)
			}

			// Node doesn't exist locally - create it
			err = h.createMissingSplitNode(ctx, db, node, nodeUUID)
			if err != nil {
				return err
			}
		}
	}

	// Phase 2: Update existing nodes
	// Now that all missing nodes are created, we can safely update parent references
	for existingNodeId, existingNode := range existingNodeMap {
		node, ok := goodNodeIDMap[existingNodeId.String()]
		if !ok {
			return fmt.Errorf("node %s not found in response", existingNodeId)
		}

		err = h.updateExistingNode(ctx, existingNode, node, existingNodeId)
		if err != nil {
			return err
		}
	}

	return nil
}

func (h *SyncNodeHandler) updateExistingNode(ctx context.Context, existingNode *ent.TreeNode, node *pb.TreeNode, nodeUUID uuid.UUID) error {
	logger := logging.GetLoggerFromContext(ctx)
	mut := existingNode.Update()

	// Check and update RawTx if changed
	if string(existingNode.RawTx) != string(node.NodeTx) {
		mut.SetRawTx(node.NodeTx)
		logger.Info("updated field RawTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update RawRefundTx if changed
	if string(existingNode.RawRefundTx) != string(node.RefundTx) {
		mut.SetRawRefundTx(node.RefundTx)
		logger.Info("updated field RawRefundTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update DirectTx if changed
	if string(existingNode.DirectTx) != string(node.DirectTx) {
		mut.SetDirectTx(node.DirectTx)
		logger.Info("updated field DirectTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update DirectRefundTx if changed
	if string(existingNode.DirectRefundTx) != string(node.DirectRefundTx) {
		mut.SetDirectRefundTx(node.DirectRefundTx)
		logger.Info("updated field DirectRefundTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update DirectFromCpfpRefundTx if changed
	if string(existingNode.DirectFromCpfpRefundTx) != string(node.DirectFromCpfpRefundTx) {
		mut.SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx)
		logger.Info("updated field DirectFromCpfpRefundTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update ParentID if changed
	if node.ParentNodeId != nil {
		parentUUID, err := uuid.Parse(node.GetParentNodeId())
		if err != nil {
			return fmt.Errorf("unable to parse parent node id %s: %w", node.GetParentNodeId(), err)
		}
		if existingNode.Edges.Parent == nil || existingNode.Edges.Parent.ID != parentUUID {
			// Validate parent node exists before setting to prevent FK violation
			db, err := ent.GetDbFromContext(ctx)
			if err != nil {
				return fmt.Errorf("failed to get db context: %w", err)
			}
			parentExists, err := db.TreeNode.Query().Where(treenode.IDEQ(parentUUID)).Exist(ctx)
			if err != nil {
				return fmt.Errorf("failed to check parent node existence: %w", err)
			}
			if !parentExists {
				return errors.NotFoundMissingEntity(
					fmt.Errorf("parent node %s does not exist, cannot update node %s", parentUUID, nodeUUID))
			}
			mut.SetParentID(parentUUID)
			logger.Info("updated field ParentID", zap.Stringer("node_id", nodeUUID))
		}
	}

	_, err := mut.Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update node %s: %w", nodeUUID, err)
	}

	return nil
}

func (h *SyncNodeHandler) createMissingSplitNode(ctx context.Context, db *ent.Client, node *pb.TreeNode, nodeUUID uuid.UUID) error {
	// Get the Tree entity
	treeUUID, err := uuid.Parse(node.TreeId)
	if err != nil {
		return fmt.Errorf("unable to parse tree id %s: %w", node.TreeId, err)
	}
	tree, err := db.Tree.Get(ctx, treeUUID)
	if err != nil {
		return fmt.Errorf("unable to get tree %s for node %s: %w", node.TreeId, node.Id, err)
	}

	// Get the SigningKeyshare entity - assume it's included in the response
	if node.SigningKeyshare == nil {
		return fmt.Errorf("signing keyshare not included for node %s", node.Id)
	}

	// Query for existing keyshare by public key
	keysharePublicKey, err := keys.ParsePublicKey(node.SigningKeyshare.PublicKey)
	if err != nil {
		return fmt.Errorf("unable to parse keyshare public key for node %s: %w", node.Id, err)
	}

	signingKeyshareEnt, err := db.SigningKeyshare.Query().
		Where(signingkeyshare.PublicKeyEQ(keysharePublicKey)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("unable to find signing keyshare for node %s: %w", node.Id, err)
	}

	// Parse public keys
	verifyingPubkey, err := keys.ParsePublicKey(node.VerifyingPublicKey)
	if err != nil {
		return fmt.Errorf("unable to parse verifying public key for node %s: %w", node.Id, err)
	}
	ownerIdentityPubkey, err := keys.ParsePublicKey(node.OwnerIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("unable to parse owner identity public key for node %s: %w", node.Id, err)
	}
	ownerSigningPubkey, err := keys.ParsePublicKey(node.OwnerSigningPublicKey)
	if err != nil {
		return fmt.Errorf("unable to parse owner signing public key for node %s: %w", node.Id, err)
	}

	// Convert status
	status := st.TreeNodeStatus(node.Status)

	// Create the node
	createBuilder := db.TreeNode.Create().
		SetID(nodeUUID).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetStatus(status).
		SetValue(node.Value).
		SetVerifyingPubkey(verifyingPubkey).
		SetOwnerIdentityPubkey(ownerIdentityPubkey).
		SetOwnerSigningPubkey(ownerSigningPubkey).
		SetSigningKeyshare(signingKeyshareEnt).
		SetRawTx(node.NodeTx).
		SetVout(int16(node.Vout))

	if node.DirectTx != nil {
		createBuilder.SetDirectTx(node.DirectTx)
	}

	// Set parent if exists, with FK validation
	if node.ParentNodeId != nil {
		parentUUID, err := uuid.Parse(node.GetParentNodeId())
		if err != nil {
			return fmt.Errorf("unable to parse parent node id %s: %w", node.GetParentNodeId(), err)
		}
		// Validate parent node exists before setting to prevent FK violation
		parentExists, err := db.TreeNode.Query().Where(treenode.IDEQ(parentUUID)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to check parent node existence: %w", err)
		}
		if !parentExists {
			return errors.NotFoundMissingEntity(
				fmt.Errorf("parent node %s does not exist, cannot create node %s", parentUUID, nodeUUID))
		}
		createBuilder.SetParentID(parentUUID)
	}

	_, err = createBuilder.Save(ctx)
	if err != nil {
		// Handle pkey violation as AlreadyExists (race condition)
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil
		}
		return fmt.Errorf("unable to create node %s: %w", node.Id, err)
	}

	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Created missing split node %d", zap.Stringer("nodeId", nodeUUID))

	return nil
}
