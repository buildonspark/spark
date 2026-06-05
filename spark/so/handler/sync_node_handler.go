package handler

import (
	"context"
	stdsql "database/sql"
	errs "errors"
	"fmt"

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
	if len(req.GetNodeIds()) == 0 || len(req.GetNodeIds()) > 100 {
		return fmt.Errorf("invalid node ids: %v", req.GetNodeIds())
	}

	operator, ok := h.config.SigningOperatorMap[req.GetOperatorId()]
	if !ok {
		return fmt.Errorf("operator %s not found", req.GetOperatorId())
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

	conn, err := operator.NewOperatorGRPCConnection()
	if err != nil {
		return fmt.Errorf("failed to get operator grpc connection: %w", err)
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)
	resp, err := client.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: req.GetNodeIds(),
			},
		},
		IncludeParents: false,
	})
	if err != nil {
		return fmt.Errorf("failed to query nodes: %w", err)
	}

	if len(resp.GetNodes()) != len(req.GetNodeIds()) {
		return fmt.Errorf("expected %d nodes, got %d", len(req.GetNodeIds()), len(resp.GetNodes()))
	}

	goodNodeIDMap := make(map[string]*pb.TreeNode)
	for _, node := range resp.GetNodes() {
		goodNodeIDMap[node.GetId()] = node
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
			if node.GetStatus() != "SPLITTED" && node.GetStatus() != "SPLIT_LOCKED" {
				return fmt.Errorf("cannot create node %s with status %s: only SPLITTED or SPLIT_LOCKED nodes can be created during sync", node.GetId(), node.GetStatus())
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
	if string(existingNode.RawTx) != string(node.GetNodeTx()) {
		mut.SetRawTx(node.GetNodeTx())
		logger.Info("updated field RawTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update RawRefundTx if changed
	if string(existingNode.RawRefundTx) != string(node.GetRefundTx()) {
		mut.SetRawRefundTx(node.GetRefundTx())
		logger.Info("updated field RawRefundTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update DirectTx if changed
	if string(existingNode.DirectTx) != string(node.GetDirectTx()) {
		mut.SetDirectTx(node.GetDirectTx())
		logger.Info("updated field DirectTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update DirectRefundTx if changed
	if string(existingNode.DirectRefundTx) != string(node.GetDirectRefundTx()) {
		mut.SetDirectRefundTx(node.GetDirectRefundTx())
		logger.Info("updated field DirectRefundTx", zap.Stringer("node_id", nodeUUID))
	}

	// Check and update DirectFromCpfpRefundTx if changed
	if string(existingNode.DirectFromCpfpRefundTx) != string(node.GetDirectFromCpfpRefundTx()) {
		mut.SetDirectFromCpfpRefundTx(node.GetDirectFromCpfpRefundTx())
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
	treeUUID, err := uuid.Parse(node.GetTreeId())
	if err != nil {
		return fmt.Errorf("unable to parse tree id %s: %w", node.GetTreeId(), err)
	}
	tree, err := db.Tree.Get(ctx, treeUUID)
	if err != nil {
		return fmt.Errorf("unable to get tree %s for node %s: %w", node.GetTreeId(), node.GetId(), err)
	}

	// Get the SigningKeyshare entity - assume it's included in the response
	if node.GetSigningKeyshare() == nil {
		return fmt.Errorf("signing keyshare not included for node %s", node.GetId())
	}

	// Query for existing keyshare by public key
	keysharePublicKey, err := keys.ParsePublicKey(node.GetSigningKeyshare().GetPublicKey())
	if err != nil {
		return fmt.Errorf("unable to parse keyshare public key for node %s: %w", node.GetId(), err)
	}

	signingKeyshareEnt, err := db.SigningKeyshare.Query().
		Where(signingkeyshare.PublicKeyEQ(keysharePublicKey)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("unable to find signing keyshare for node %s: %w", node.GetId(), err)
	}

	// Parse public keys
	verifyingPubkey, err := keys.ParsePublicKey(node.GetVerifyingPublicKey())
	if err != nil {
		return fmt.Errorf("unable to parse verifying public key for node %s: %w", node.GetId(), err)
	}
	ownerIdentityPubkey, err := keys.ParsePublicKey(node.GetOwnerIdentityPublicKey())
	if err != nil {
		return fmt.Errorf("unable to parse owner identity public key for node %s: %w", node.GetId(), err)
	}
	ownerSigningPubkey, err := keys.ParsePublicKey(node.GetOwnerSigningPublicKey())
	if err != nil {
		return fmt.Errorf("unable to parse owner signing public key for node %s: %w", node.GetId(), err)
	}

	// Convert status
	status := st.TreeNodeStatus(node.GetStatus())

	// Create the node
	createBuilder := db.TreeNode.Create().
		SetID(nodeUUID).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetStatus(status).
		SetValue(node.GetValue()).
		SetVerifyingPubkey(verifyingPubkey).
		SetOwnerIdentityPubkey(ownerIdentityPubkey).
		SetOwnerSigningPubkey(ownerSigningPubkey).
		SetSigningKeyshare(signingKeyshareEnt).
		SetRawTx(node.GetNodeTx()).
		SetVout(int16(node.GetVout()))

	if node.DirectTx != nil {
		createBuilder.SetDirectTx(node.GetDirectTx())
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

	err = createBuilder.
		OnConflictColumns(treenode.FieldID).
		DoNothing().
		Exec(ctx)
	// ON CONFLICT DO NOTHING returns 0 rows on the (expected) idempotent race,
	// which Ent surfaces as stdsql.ErrNoRows; that path is a no-op, not a create.
	logger := logging.GetLoggerFromContext(ctx)
	switch {
	case err == nil:
		logger.Info("Created missing split node", zap.Stringer("nodeId", nodeUUID))
	case errs.Is(err, stdsql.ErrNoRows):
		logger.Debug("skipped creating node that was concurrently created", zap.Stringer("nodeId", nodeUUID))
	default:
		return fmt.Errorf("unable to create node %s: %w", node.GetId(), err)
	}

	return nil
}
