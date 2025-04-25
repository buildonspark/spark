package handler

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
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
	logger := logging.GetLoggerFromContext(ctx)

	nodeID, err := uuid.Parse(req.NodeId)
	if err != nil {
		logger.Error("Failed to parse node ID", "error", err)
		return nil, err
	}
	err = ent.MarkNodeAsLocked(ctx, nodeID, schema.TreeNodeStatusSplitLocked)
	if err != nil {
		logger.Error("Failed to mark node as locked", "error", err)
		return nil, err
	}
	selectedKeyshares := make([]uuid.UUID, len(req.SelectedKeyshareIds)+1)
	u, err := uuid.Parse(req.TargetKeyshareId)
	if err != nil {
		logger.Error("Failed to parse target keyshare ID", "error", err)
		return nil, err
	}
	selectedKeyshares[0] = u

	for i, id := range req.SelectedKeyshareIds {
		u, err := uuid.Parse(id)
		if err != nil {
			logger.Error("Failed to parse keyshare ID", "error", err)
			return nil, err
		}
		selectedKeyshares[i+1] = u
	}

	_, err = ent.MarkSigningKeysharesAsUsed(ctx, h.config, selectedKeyshares)
	if err != nil {
		logger.Error("Failed to mark keyshares as used", "error", err)
		return nil, err
	}

	keyShares, err := ent.GetKeyPackagesArray(ctx, selectedKeyshares)
	if err != nil {
		logger.Error("Failed to get key shares", "error", err)
		return nil, err
	}

	lastKeyshareID, err := uuid.Parse(req.LastKeyshareId)
	if err != nil {
		logger.Error("Failed to parse last keyshare ID", "error", err)
		return nil, err
	}

	_, err = ent.CalculateAndStoreLastKey(ctx, h.config, keyShares[0], keyShares[1:], lastKeyshareID)
	if err != nil {
		logger.Error("Failed to calculate and store last key share", "error", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}
