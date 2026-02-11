package tokens

import (
	"context"

	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
)

type InternalFreezeTokenHandler struct {
	config *so.Config
}

func NewInternalFreezeTokenHandler(config *so.Config) *InternalFreezeTokenHandler {
	return &InternalFreezeTokenHandler{
		config: config,
	}
}

// InternalFreezeTokens performs full independent validation - does NOT trust the coordinator.
func (h *InternalFreezeTokenHandler) InternalFreezeTokens(
	ctx context.Context,
	req *tokeninternalpb.InternalFreezeTokensRequest,
) (*tokeninternalpb.InternalFreezeTokensResponse, error) {
	result, err := ValidateAndApplyFreeze(ctx, h.config, req.FreezeTokensPayload, req.IssuerSignature)
	if err != nil {
		return nil, err
	}

	return &tokeninternalpb.InternalFreezeTokensResponse{
		ImpactedTokenOutputs: result.OutputRefs,
		ImpactedTokenAmount:  result.TotalAmount,
	}, nil
}
