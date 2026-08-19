package ent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/ent/tokenallowancespend"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// GetAllowanceByAllowanceID returns the TokenAllowance for the given client allowance ID.
func GetAllowanceByAllowanceID(ctx context.Context, allowanceID uuid.UUID) (*TokenAllowance, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return db.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(ctx)
}

// GetAllowanceByAllowanceIDForUpdate returns the TokenAllowance for the given client
// allowance ID with a FOR UPDATE lock. Use this when metering a spend so concurrent
// spends against the same allowance serialize and cannot overspend the limit.
func GetAllowanceByAllowanceIDForUpdate(ctx context.Context, allowanceID uuid.UUID) (*TokenAllowance, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return db.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).ForUpdate().Only(ctx)
}

// GetReservedSpendsForAllowanceForUpdate returns the RESERVED spends metered against the
// allowance with the given client allowance ID, locked FOR UPDATE and with the associated
// token transaction eager-loaded.
func GetReservedSpendsForAllowanceForUpdate(ctx context.Context, allowanceID uuid.UUID) ([]*TokenAllowanceSpend, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	spends, err := db.TokenAllowanceSpend.Query().
		Where(
			tokenallowancespend.StatusEQ(st.TokenAllowanceSpendStatusReserved),
			tokenallowancespend.HasTokenAllowanceWith(tokenallowance.AllowanceID(allowanceID)),
		).
		WithTokenTransaction().
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch reserved spends for allowance_id %s: %w", allowanceID, err))
	}
	return spends, nil
}
