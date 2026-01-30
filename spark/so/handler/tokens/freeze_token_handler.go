package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/logging"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
)

type FreezeTokenHandler struct {
	config *so.Config
}

func NewFreezeTokenHandler(config *so.Config) *FreezeTokenHandler {
	return &FreezeTokenHandler{
		config: config,
	}
}

// FreezeTokens freezes or unfreezes tokens on the LRC20 node.
// When coordinated freeze is enabled, this handler acts as coordinator and fans out
// the freeze request to all other SOs before applying locally.
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

	// Apply freeze locally first
	result, err := ValidateAndApplyFreeze(ctx, h.config, req.FreezeTokensPayload, req.IssuerSignature)
	if err != nil {
		return nil, err
	}

	var freezeProgress *tokenpb.FreezeProgress

	knobService := knobs.GetKnobsService(ctx)
	coordinatedFreezeEnabled := knobService != nil && knobService.RolloutRandom(knobs.KnobCoordinatedFreezeEnabled, 0)
	if coordinatedFreezeEnabled {
		// Commit the transaction before fanning out so we don't hold the lock during network calls
		if err := ent.DbCommit(ctx); err != nil {
			return nil, errors.InternalDatabaseWriteError(fmt.Errorf("failed to commit freeze transaction: %w", err))
		}

		freezeProgress = h.fanOutFreezeToOtherOperators(ctx, req)
		freezeProgress.FrozenOperatorPublicKeys = append(
			freezeProgress.FrozenOperatorPublicKeys,
			h.config.IdentityPublicKey().Serialize(),
		)
	}

	return &tokenpb.FreezeTokensResponse{
		ImpactedTokenOutputs: result.OutputRefs,
		ImpactedTokenAmount:  result.TotalAmount,
		FreezeProgress:       freezeProgress,
	}, nil
}

// fanOutFreezeToOtherOperators sends freeze requests to all other operators.
// Returns progress showing which operators succeeded/failed.
func (h *FreezeTokenHandler) fanOutFreezeToOtherOperators(ctx context.Context, req *tokenpb.FreezeTokensRequest) *tokenpb.FreezeProgress {
	logger := logging.GetLoggerFromContext(ctx)

	excludeSelf := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	results, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &excludeSelf,
		func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.InternalFreezeTokensResponse, error) {
			conn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := tokeninternalpb.NewSparkTokenInternalServiceClient(conn)
			return client.InternalFreezeTokens(ctx, &tokeninternalpb.InternalFreezeTokensRequest{
				FreezeTokensPayload: req.FreezeTokensPayload,
				IssuerSignature:     req.IssuerSignature,
			})
		},
	)
	if err != nil {
		logger.Warn("coordinated freeze fan-out had failures",
			zap.Error(err),
			zap.Binary("token_identifier", req.FreezeTokensPayload.GetTokenIdentifier()),
		)
	}

	progress := h.buildFreezeProgress(results)

	return progress
}

// buildFreezeProgress builds a FreezeProgress from the fan-out results.
// Operators with non-nil results are considered frozen, others are unfrozen.
func (h *FreezeTokenHandler) buildFreezeProgress(results map[string]*tokeninternalpb.InternalFreezeTokensResponse) *tokenpb.FreezeProgress {
	var frozen, unfrozen [][]byte

	for identifier, operator := range h.config.SigningOperatorMap {
		if identifier == h.config.Identifier {
			continue // Self is handled separately in FreezeTokens
		}

		result, exists := results[identifier]
		if exists && result != nil {
			frozen = append(frozen, operator.IdentityPublicKey.Serialize())
		} else {
			unfrozen = append(unfrozen, operator.IdentityPublicKey.Serialize())
		}
	}

	return &tokenpb.FreezeProgress{
		FrozenOperatorPublicKeys:   frozen,
		UnfrozenOperatorPublicKeys: unfrozen,
	}
}
