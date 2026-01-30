package tokens

import (
	"context"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
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
	issuerPubKey, err := GetIssuerPublicKeyForFreeze(ctx, req.FreezeTokensPayload.GetTokenIdentifier())
	if err != nil {
		return nil, err
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, *issuerPubKey); err != nil {
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
