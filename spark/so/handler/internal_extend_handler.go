package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

// InternalExtendLeafHandler is the extend leaf handler for so internal.
type InternalExtendLeafHandler struct {
	config *so.Config
}

// NewInternalExtendLeafHandler creates a new InternalExtendLeafHandler.
func NewInternalExtendLeafHandler(config *so.Config) *InternalExtendLeafHandler {
	return &InternalExtendLeafHandler{
		config: config,
	}
}

// FinalizeExtendLeaf finalizes an extend leaf.
// This creates the new node and nullifies the refund tx of the parent.
func (h *InternalExtendLeafHandler) FinalizeExtendLeaf(ctx context.Context, req *pbinternal.FinalizeExtendLeafRequest) error {
	db := ent.GetDbFromContext(ctx)

	node := req.Node
	nodeID, err := uuid.Parse(node.Id)
	if err != nil {
		return fmt.Errorf("failed to parse node id: %w", err)
	}
	treeID, err := uuid.Parse(node.TreeId)
	if err != nil {
		return fmt.Errorf("failed to parse tree id: %w", err)
	}
	signingKeyshareID, err := uuid.Parse(node.SigningKeyshareId)
	if err != nil {
		return fmt.Errorf("failed to parse signing keyshare id: %w", err)
	}
	parentID, err := uuid.Parse(*node.ParentNodeId)
	if err != nil {
		return fmt.Errorf("failed to parse parent node id: %w", err)
	}
	_, err = db.
		TreeNode.
		Create().
		SetID(nodeID).
		SetTreeID(treeID).
		SetStatus(schema.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(node.OwnerSigningPubkey).
		SetValue(node.Value).
		SetVerifyingPubkey(node.VerifyingPubkey).
		SetSigningKeyshareID(signingKeyshareID).
		SetRawTx(node.RawTx).
		SetRawRefundTx(node.RawRefundTx).
		SetVout(int16(0)).
		SetParentID(parentID).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to create new node: %w", err)
	}

	_, err = db.
		TreeNode.
		UpdateOneID(parentID).
		SetRawRefundTx(nil).
		SetStatus(schema.TreeNodeStatusSplitted).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to nullify refund tx: %w", err)
	}

	return nil
}
