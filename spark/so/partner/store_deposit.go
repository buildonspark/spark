package partner

import (
	"context"
	dbSql "database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddresspartner"
	"github.com/lightsparkdev/spark/so/knobs"
)

// SaveDepositAddressPartner creates a DepositAddressPartner record
// linking a deposit address to the partner from the request context.
// Only runs when the partner JWT knob is enabled and partner info is present.
// Failures are logged but never block the caller.
func SaveDepositAddressPartner(ctx context.Context, depositAddressID uuid.UUID) {
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobEnablePartnerJWT, 0) == 0 {
		return
	}

	pInfo, ok := GetPartnerInfoFromContext(ctx)
	if !ok || pInfo.PartnerDBID == uuid.Nil {
		return
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to get db context for deposit address partner: %v", err)
		return
	}

	err = db.DepositAddressPartner.Create().
		SetPartnerID(pInfo.PartnerDBID).
		SetDepositAddressID(depositAddressID).
		OnConflictColumns(depositaddresspartner.DepositAddressColumn).
		Ignore().
		Exec(ctx)
	if err != nil && !errors.Is(err, dbSql.ErrNoRows) {
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to save deposit address partner for deposit address %s: %v", depositAddressID, err)
	}
}
