package handler

import (
	"context"
	"log"

	"github.com/google/uuid"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/entutils"
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
	err = entutils.MarkNodeAsLocked(ctx, nodeID, schema.TreeNodeStatusSplitLocked)
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

	err = entutils.MarkSigningKeysharesAsUsed(ctx, h.config, selectedKeyshares)
	if err != nil {
		log.Printf("Failed to mark keyshares as used: %v", err)
		return nil, err
	}

	keyShares, err := entutils.GetKeyPackagesArray(ctx, selectedKeyshares)
	if err != nil {
		log.Printf("Failed to get key shares: %v", err)
		return nil, err
	}

	lastKeyshareID, err := uuid.Parse(req.LastKeyshareId)
	if err != nil {
		log.Printf("Failed to parse last keyshare ID: %v", err)
		return nil, err
	}

	_, err = entutils.CalculateAndStoreLastKey(ctx, h.config, keyShares[0], keyShares[1:], lastKeyshareID)
	if err != nil {
		log.Printf("Failed to calculate and store last key share: %v", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}
