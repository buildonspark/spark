package partner

import (
	"context"
	dbSql "database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferpartner"
)

// SaveTransferPartner creates a TransferPartner record on the coordinator linking
// the transfer to the partner from the request context.
// If no partner info is present in the context, this is a no-op.
// Failures are logged but never block the caller.
func SaveTransferPartner(ctx context.Context, transferID uuid.UUID, transferPartnerType schematype.TransferPartnerType) {
	pInfo, ok := GetPartnerInfoFromContext(ctx)
	if !ok {
		return
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to get db context for transfer partner: %w", err)
		return
	}

	err = db.TransferPartner.Create().
		SetPartnerID(pInfo.PartnerDBID).
		SetTransferID(transferID).
		SetType(transferPartnerType).
		OnConflictColumns(transferpartner.TransferColumn).
		Ignore().
		Exec(ctx)
	if err != nil && !errors.Is(err, dbSql.ErrNoRows) {
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to save transfer partner for transfer %s: %w", transferID, err)
	}
}
