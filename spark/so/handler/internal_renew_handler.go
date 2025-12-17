package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"go.uber.org/zap"
)

// InternalRenewLeafHandler is the extend leaf handler for so internal.
type InternalRenewLeafHandler struct {
	config *so.Config
}

// NewInternalRenewLeafHandler creates a new InternalExtendLeafHandler.
func NewInternalRenewLeafHandler(config *so.Config) *InternalRenewLeafHandler {
	return &InternalRenewLeafHandler{
		config: config,
	}
}

// FinalizeRenewNodeTimelock finalizes a renew leaf operation.
// This creates the new split node and updates the extended leaf.
func (h *InternalRenewLeafHandler) FinalizeRenewNodeTimelock(ctx context.Context, req *pbinternal.FinalizeRenewNodeTimelockRequest) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Validate extended leaf status before any database writes
	extendedLeafID, err := uuid.Parse(req.Node.Id)
	if err != nil {
		return fmt.Errorf("failed to parse extended leaf id %v: %w", req.Node.Id, err)
	}

	extendedLeafNode, err := db.TreeNode.Query().Where(treenode.ID(extendedLeafID)).ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query extended leaf node %s: %w", extendedLeafID, err)
	}

	if extendedLeafNode.Status != st.TreeNodeStatusAvailable {
		return fmt.Errorf("extended leaf node %s must have status Available, but has status %s", extendedLeafID, extendedLeafNode.Status)
	}

	// Process the split node (newly created node) - first node
	splitNode := req.SplitNode
	splitNodeID, err := uuid.Parse(splitNode.Id)
	if err != nil {
		return fmt.Errorf("failed to parse split node %s: %w", splitNode.Id, err)
	}
	splitTreeID, err := uuid.Parse(splitNode.TreeId)
	if err != nil {
		return fmt.Errorf("failed to parse split tree %s: %w", splitNode.Id, err)
	}
	splitSigningKeyshareID, err := uuid.Parse(splitNode.SigningKeyshareId)
	if err != nil {
		return fmt.Errorf("failed to parse split signing keyshare id: %w", err)
	}
	splitParentID := uuid.Nil
	if splitNode.ParentNodeId != nil {
		parentID, err := uuid.Parse(splitNode.GetParentNodeId())
		if err != nil {
			return fmt.Errorf("failed to parse split parent node id: %w", err)
		}
		splitParentID = parentID
	}

	ownerIdentityPubKey, err := keys.ParsePublicKey(splitNode.GetOwnerIdentityPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse owner identity pubkey: %w", err)
	}
	ownerSigningPubKey, err := keys.ParsePublicKey(splitNode.GetOwnerSigningPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	verifyingPubKey, err := keys.ParsePublicKey(splitNode.GetVerifyingPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse verifying pubkey: %w", err)
	}

	// TODO(mhr): Remove this when the transfer proto has Network and it has been backfilled.
	treeEnt, err := db.Tree.Query().Where(tree.IDEQ(splitTreeID)).Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query tree %s: %w", splitTreeID, err)
	}

	// Create the split node
	splitNodeMut := db.TreeNode.Create().
		SetID(splitNodeID).
		SetTreeID(splitTreeID).
		SetNetwork(treeEnt.Network).
		SetStatus(st.TreeNodeStatusSplitLocked).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetValue(splitNode.Value).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshareID(splitSigningKeyshareID).
		SetRawTx(splitNode.RawTx).
		SetDirectTx(splitNode.DirectTx).
		SetVout(int16(splitNode.Vout))
	if splitParentID != uuid.Nil {
		splitNodeMut.SetParentID(splitParentID)
	}
	_, err = splitNodeMut.Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to create split node %s: %w", splitNodeID, err)
	}
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Created split node", zap.String("split_node_id", splitNodeID.String()))

	extendedLeaf := req.Node
	_, err = extendedLeafNode.Update().
		SetRawTx(extendedLeaf.RawTx).
		SetRawRefundTx(extendedLeaf.RawRefundTx).
		SetDirectTx(extendedLeaf.DirectTx).
		SetDirectRefundTx(extendedLeaf.DirectRefundTx).
		SetDirectFromCpfpRefundTx(extendedLeaf.DirectFromCpfpRefundTx).
		SetParentID(splitNodeID).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update extended leaf %s: %w", extendedLeafID, err)
	}
	logger.Info("Updated extended leaf",
		zap.String("extended_leaf_id", extendedLeafID.String()),
		zap.String("split_node_id", splitNodeID.String()))
	return nil
}

// FinalizeRenewRefundTimelock finalizes a renew refund timelock operation.
// This only updates the existing leaf node without creating a split node.
func (h *InternalRenewLeafHandler) FinalizeRenewRefundTimelock(ctx context.Context, req *pbinternal.FinalizeRenewRefundTimelockRequest) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Validate leaf status before any database writes
	leafID, err := uuid.Parse(req.GetNode().GetId())
	if err != nil {
		return fmt.Errorf("failed to parse leaf %s: %w", req.GetNode().GetId(), err)
	}

	leafNode, err := db.TreeNode.Query().Where(treenode.ID(leafID)).ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query leaf %v: %w", leafID, err)
	}

	if leafNode.Status != st.TreeNodeStatusAvailable {
		return fmt.Errorf("leaf node %s must have status Available, but has status %s", leafID, leafNode.Status)
	}

	leaf := req.Node
	_, err = leafNode.Update().
		SetRawTx(leaf.RawTx).
		SetRawRefundTx(leaf.RawRefundTx).
		SetDirectTx(leaf.DirectTx).
		SetDirectRefundTx(leaf.DirectRefundTx).
		SetDirectFromCpfpRefundTx(leaf.DirectFromCpfpRefundTx).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update leaf %s: %w", leafID, err)
	}

	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Updated leaf for refund timelock renewal", zap.String("leaf_id", leafID.String()))
	return nil
}
