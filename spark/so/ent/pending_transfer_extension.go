package ent

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// CreateOrResetPendingSendTransfer creates a pending send transfer for a given transfer id.
// If the pending send transfer already exists, it will be updated with the pending status.
func CreateOrResetPendingSendTransfer(ctx context.Context, transferID uuid.UUID) (*PendingSendTransfer, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	pendingTransfer, err := db.PendingSendTransfer.Query().Where(pendingsendtransfer.TransferID(transferID)).Only(ctx)
	if IsNotFound(err) {
		return db.PendingSendTransfer.Create().SetTransferID(transferID).SetStatus(st.PendingSendTransferStatusPending).Save(ctx)
	}

	return pendingTransfer.Update().SetStatus(st.PendingSendTransferStatusPending).Save(ctx)
}
