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
