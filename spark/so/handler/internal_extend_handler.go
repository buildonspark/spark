package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
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
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

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

	ownerIdentityPubKey, err := keys.ParsePublicKey(node.GetOwnerIdentityPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse owner identity pubkey: %w", err)
	}
	ownerSigningPubKey, err := keys.ParsePublicKey(node.GetOwnerSigningPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	verifyingPubKey, err := keys.ParsePublicKey(node.GetVerifyingPubkey())
	if err != nil {
		return fmt.Errorf("failed to parse verifying pubkey: %w", err)
	}

	_, err = db.
		TreeNode.
		Create().
		SetID(nodeID).
		SetTreeID(treeID).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetValue(node.Value).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshareID(signingKeyshareID).
		SetRawTx(node.RawTx).
		SetRawRefundTx(node.RawRefundTx).
		SetDirectTx(node.DirectTx).
		SetDirectRefundTx(node.DirectRefundTx).
		SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx).
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
		SetDirectRefundTx(nil).
		SetDirectFromCpfpRefundTx(nil).
		SetStatus(st.TreeNodeStatusSplitted).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to nullify refund tx: %w", err)
	}

	return nil
}
