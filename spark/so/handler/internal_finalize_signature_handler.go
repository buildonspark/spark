package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"google.golang.org/protobuf/types/known/emptypb"
)

// InternalFinalizeSignatureHandler is the handler for the InternalFinalizeNodeSignatures RPC.
type InternalFinalizeSignatureHandler struct {
	config *so.Config
}

// NewInternalFinalizeSignatureHandler creates a new InternalFinalizeSignatureHandler.
func NewInternalFinalizeSignatureHandler(config *so.Config) *InternalFinalizeSignatureHandler {
	return &InternalFinalizeSignatureHandler{config: config}
}

// InternalFinalizeNodeSignatures verifies the node signatures and updates the node.
func (h *InternalFinalizeSignatureHandler) InternalFinalizeNodeSignatures(ctx context.Context, req *pbinternal.InternalFinalizeNodeSignaturesRequest) (*emptypb.Empty, error) {
	for _, node := range req.Nodes {
		err := h.updateNode(ctx, node, req.Intent)
		if err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}

func (h *InternalFinalizeSignatureHandler) updateNode(ctx context.Context, node *pbinternal.TreeNode, intent pbcommon.SignatureIntent) error {
	db := common.GetDbFromContext(ctx)
	treeID, err := uuid.Parse(node.TreeId)
	if err != nil {
		return err
	}
	nodeID, err := uuid.Parse(node.Id)
	if err != nil {
		return err
	}
	signingKeyshareID, err := uuid.Parse(node.SigningKeyshareId)
	if err != nil {
		return err
	}
	if intent == pbcommon.SignatureIntent_CREATION || intent == pbcommon.SignatureIntent_SPLIT {
		var tree *ent.Tree
		if intent == pbcommon.SignatureIntent_CREATION {
			tree = db.Tree.Create().SetID(treeID).SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).SaveX(ctx)
		} else {
			tree, err = db.Tree.Get(ctx, treeID)
			if err != nil {
				return err
			}
		}
		root := db.TreeNode.
			Create().
			SetID(nodeID).
			SetTree(tree).
			SetStatus(schema.TreeNodeStatusAvailable).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(node.OwnerSigningPubkey).
			SetValue(node.Value).
			SetVerifyingPubkey(node.VerifyingPubkey).
			SetSigningKeyshareID(signingKeyshareID).
			SetVout(uint16(node.Vout)).
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx).
			SaveX(ctx)
		tree.Update().SetRoot(root).SaveX(ctx)
	} else {
		node, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return err
		}
		if node == nil {
			return fmt.Errorf("node not found")
		}
		node = node.Update().
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx).
			SetStatus(schema.TreeNodeStatusAvailable).
			SaveX(ctx)
	}
	return nil
}
