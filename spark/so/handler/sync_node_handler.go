package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbin "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/treenode"
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
	nodeUuids := []uuid.UUID{}
	for _, nodeId := range req.NodeIds {
		nodeUuid, err := uuid.Parse(nodeId)
		if err != nil {
			return fmt.Errorf("unable to parse node id %s: %w", nodeId, err)
		}
		nodeUuids = append(nodeUuids, nodeUuid)
	}
	nodes, err := db.TreeNode.Query().Where(treenode.IDIn(nodeUuids...)).ForUpdate().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to lock tree nodes %v: %w", nodeUuids, err)
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

	nodeIDMap := make(map[string]*pb.TreeNode)
	for _, node := range resp.Nodes {
		nodeIDMap[node.Id] = node
	}

	// Create a map of existing node UUIDs for quick lookup
	existingNodeMap := make(map[uuid.UUID]*ent.TreeNode)
	for _, node := range nodes {
		existingNodeMap[node.ID] = node
	}

	for _, nodeUUID := range nodeUuids {
		node, ok := nodeIDMap[nodeUUID.String()]
		if !ok {
			return fmt.Errorf("node %s not found in response", nodeUUID.String())
		}

		existingNode, exists := existingNodeMap[nodeUUID]
		if exists {
			// Node exists - update transaction fields
			mut := existingNode.Update().
				SetRawTx(node.NodeTx).
				SetRawRefundTx(node.RefundTx).
				SetDirectTx(node.DirectTx).
				SetDirectRefundTx(node.DirectRefundTx).
				SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx)

			if node.ParentNodeId != nil {
				parentUUID, err := uuid.Parse(*node.ParentNodeId)
				if err != nil {
					return fmt.Errorf("unable to parse parent node id %s: %w", *node.ParentNodeId, err)
				}
				mut.SetParentID(parentUUID)
			}

			_, err = mut.Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update node %s: %w", nodeUUID.String(), err)
			}
		} else {
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

	// Set parent if exists
	if node.ParentNodeId != nil {
		parentUUID, err := uuid.Parse(*node.ParentNodeId)
		if err != nil {
			return fmt.Errorf("unable to parse parent node id %s: %w", *node.ParentNodeId, err)
		}
		createBuilder.SetParentID(parentUUID)
	}

	_, err = createBuilder.Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to create node %s: %w", node.Id, err)
	}

	return nil
}
