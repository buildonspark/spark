package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
)

// EntityDkgKeyHandler handles entity DKG key operations
type EntityDkgKeyHandler struct {
	config *so.Config
}

// NewEntityDkgKeyHandler creates a new EntityDkgKeyHandler
func NewEntityDkgKeyHandler(config *so.Config) *EntityDkgKeyHandler {
	return &EntityDkgKeyHandler{
		config: config,
	}
}

// ReserveEntityDkgKey reserves an entity DKG key for the given entity
func (h *EntityDkgKeyHandler) ReserveEntityDkgKey(ctx context.Context, req *pbinternal.ReserveEntityDkgKeyRequest) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Attempting to reserve entity DKG key %s", req.KeyshareId)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	dkgKeyUUID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		return fmt.Errorf("invalid DKG key ID format: %w", err)
	}
	existingEntityDkgKey, err := db.EntityDkgKey.Query().WithSigningKeyshare().Only(ctx)
	if err == nil {
		if existingEntityDkgKey.Edges.SigningKeyshare.ID != dkgKeyUUID {
			return fmt.Errorf("entity DKG key already reserved with different keyshare ID")
		}
		logger.Sugar().Infof("Entity DKG key %s already reserved in prior call, skipping reservation", existingEntityDkgKey.ID)
		// Don't return an error in this case so that the caller knows it was already successfully reserved in a prior call.
		return nil
	}
	if !ent.IsNotFound(err) {
		return fmt.Errorf("failed to check if entity DKG key already exists: %w", err)
	}
	_, err = ent.MarkSigningKeysharesAsUsed(ctx, h.config, []uuid.UUID{dkgKeyUUID})
	if err != nil {
		return err
	}
	entityDkgKey, err := db.EntityDkgKey.Create().
		SetSigningKeyshareID(dkgKeyUUID).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to create entity DKG key: %w", err)
	}
	logger.Sugar().Infof("Successfully created entity DKG key %s", entityDkgKey.ID)
	return nil
}
