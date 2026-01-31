package tokens

import (
	"context"
	"fmt"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/errors"
)

type FreezeTokenHandler struct {
	config *so.Config
}

// NewFreezeTokenHandler creates a new FreezeTokenHandler.
func NewFreezeTokenHandler(config *so.Config) *FreezeTokenHandler {
	return &FreezeTokenHandler{
		config: config,
	}
}

// FreezeTokens freezes or unfreezes tokens on the LRC20 node.
func (h *FreezeTokenHandler) FreezeTokens(ctx context.Context, req *tokenpb.FreezeTokensRequest) (*tokenpb.FreezeTokensResponse, error) {
	// Verify session auth - only the issuer can freeze tokens
	tokenIdentifier := req.FreezeTokensPayload.GetTokenIdentifier()
	if tokenIdentifier == nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("token identifier is required"))
	}
	tokenCreateEnt, err := ent.GetTokenCreateByIdentifier(ctx, tokenIdentifier)
	if err != nil {
		return nil, errors.NotFoundMissingEntity(fmt.Errorf("failed to get token for freeze request: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, tokenCreateEnt.IssuerPublicKey); err != nil {
		return nil, err
	}

	result, err := ValidateAndApplyFreeze(ctx, h.config, req.FreezeTokensPayload, req.IssuerSignature)
	if err != nil {
		return nil, err
	}

	return &tokenpb.FreezeTokensResponse{
		ImpactedTokenOutputs: result.OutputRefs,
		ImpactedTokenAmount:  result.TotalAmount,
	}, nil
}
