package handler

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"google.golang.org/protobuf/types/known/emptypb"
)

// InternalSplitHandler is a handler for internal split operations.
type InternalSplitHandler struct {
	config *so.Config
}

// NewInternalSplitHandler creates a new InternalSplitHandler.
func NewInternalSplitHandler(config *so.Config) *InternalSplitHandler {
	return &InternalSplitHandler{config: config}
}

// PrepareSplitKeyshares prepares the keyshares for a split.
func (h *InternalSplitHandler) PrepareSplitKeyshares(ctx context.Context, req *pbinternal.PrepareSplitKeysharesRequest) (*emptypb.Empty, error) {
	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		log.Printf("Failed to parse node ID: %v", err)
		return nil, err
	}
	err = ent.MarkNodeAsLocked(ctx, nodeID, schema.TreeNodeStatusSplitLocked)
	if err != nil {
		log.Printf("Failed to mark node as locked: %v", err)
		return nil, err
	}
	selectedKeyshares := make([]uuid.UUID, len(req.SelectedKeyshareIds)+1)
	u, err := uuid.Parse(req.TargetKeyshareId)
	if err != nil {
		log.Printf("Failed to parse target keyshare ID: %v", err)
		return nil, err
	}
	selectedKeyshares[0] = u

	for i, id := range req.SelectedKeyshareIds {
		u, err := uuid.Parse(id)
		if err != nil {
			log.Printf("Failed to parse keyshare ID: %v", err)
			return nil, err
		}
		selectedKeyshares[i+1] = u
	}

	err = ent.MarkSigningKeysharesAsUsed(ctx, h.config, selectedKeyshares)
	if err != nil {
		log.Printf("Failed to mark keyshares as used: %v", err)
		return nil, err
	}

	keyShares, err := ent.GetKeyPackagesArray(ctx, selectedKeyshares)
	if err != nil {
		log.Printf("Failed to get key shares: %v", err)
		return nil, err
	}

	lastKeyshareID, err := uuid.Parse(req.LastKeyshareId)
	if err != nil {
		log.Printf("Failed to parse last keyshare ID: %v", err)
		return nil, err
	}

	_, err = ent.CalculateAndStoreLastKey(ctx, h.config, keyShares[0], keyShares[1:], lastKeyshareID)
	if err != nil {
		log.Printf("Failed to calculate and store last key share: %v", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

// FinalizeNodeSplit finalizes the node split.
func (h *InternalSplitHandler) FinalizeNodeSplit(ctx context.Context, req *pbinternal.FinalizeNodeSplitRequest) error {
	db := ent.GetDbFromContext(ctx)
	parentNodeID, err := uuid.Parse(req.ParentNodeId)
	if err != nil {
		return err
	}
	parentNode, err := db.TreeNode.Get(ctx, parentNodeID)
	if err != nil {
		return err
	}
	if parentNode.Status != schema.TreeNodeStatusSplitLocked {
		return fmt.Errorf("parent node is not locked for split")
	}

	treeID, err := parentNode.QueryTree().OnlyID(ctx)
	if err != nil {
		return err
	}

	for _, node := range req.ChildNodes {
		if *(node.ParentNodeId) != req.ParentNodeId {
			return fmt.Errorf("parent node ID mismatch")
		}
		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return err
		}

		signingKeyshareID, err := uuid.Parse(node.SigningKeyshareId)
		if err != nil {
			return err
		}

		_, err = db.TreeNode.
			Create().
			SetID(nodeID).
			SetTreeID(treeID).
			SetParentID(parentNodeID).
			SetStatus(schema.TreeNodeStatusAvailable).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(node.OwnerSigningPubkey).
			SetValue(node.Value).
			SetVerifyingPubkey(node.VerifyingPubkey).
			SetSigningKeyshareID(signingKeyshareID).
			SetVout(uint16(node.Vout)).
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx).
			SetRefundTimelock(node.RefundTimelock).
			Save(ctx)
		if err != nil {
			return err
		}
	}

	_, err = parentNode.Update().SetStatus(schema.TreeNodeStatusSplitted).Save(ctx)
	if err != nil {
		return err
	}
	return nil
}
