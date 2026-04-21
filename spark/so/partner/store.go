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
	"github.com/lightsparkdev/spark/so/knobs"
)

// SaveTransferPartner creates a TransferPartner record on the coordinator linking
// the transfer to the partner from the request context.
// Only runs when the partner JWT knob is enabled and partner info is present.
// Failures are logged but never block the caller.
func SaveTransferPartner(ctx context.Context, transferID uuid.UUID, transferPartnerType schematype.TransferPartnerType) {
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobEnablePartnerJWT, 0) == 0 {
		return
	}

	pInfo, ok := GetPartnerInfoFromContext(ctx)
	if !ok || pInfo.PartnerDBID == uuid.Nil {
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
