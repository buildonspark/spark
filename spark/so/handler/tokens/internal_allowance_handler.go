package tokens

import (
	"context"
	"fmt"

	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/errors"
)

type InternalAllowanceHandler struct {
	config *so.Config
}

func NewInternalAllowanceHandler(config *so.Config) *InternalAllowanceHandler {
	return &InternalAllowanceHandler{
		config: config,
	}
}

// InternalCreateTokenAllowance is the SO-to-SO entry point for installing an allowance. Like the
// internal freeze handler, it performs full independent validation and does NOT trust the
// coordinator: it re-runs the same ValidateAndApply used by the public path.
func (h *InternalAllowanceHandler) InternalCreateTokenAllowance(
	ctx context.Context,
	req *tokeninternalpb.InternalCreateTokenAllowanceRequest,
) (*tokeninternalpb.InternalCreateTokenAllowanceResponse, error) {
	if !allowancesEnabled(ctx) {
		return nil, errors.UnimplementedMethodDisabled(fmt.Errorf("token allowances are not enabled"))
	}

	if err := ValidateAndApplyCreateAllowance(ctx, h.config, req.GetAllowancePayload(), req.GetOwnerSignature()); err != nil {
		return nil, err
	}

	return &tokeninternalpb.InternalCreateTokenAllowanceResponse{}, nil
}
