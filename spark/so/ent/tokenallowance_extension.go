package ent

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
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
