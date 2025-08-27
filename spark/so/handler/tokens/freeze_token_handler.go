package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/errors"

	"github.com/lightsparkdev/spark/common"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
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
	// Validate freeze tokens payload
	if err := utils.ValidateFreezeTokensPayload(req.FreezeTokensPayload, h.config.IdentityPublicKey()); err != nil {
		return nil, fmt.Errorf("freeze tokens payload validation failed: %w", err)
	}

	freezePayloadHash, err := utils.HashFreezeTokensPayload(req.FreezeTokensPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to hash freeze tokens payload: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	var tokenCreateEnt *ent.TokenCreate
	if req.FreezeTokensPayload.TokenIdentifier != nil {
		if err != nil {
			return nil, fmt.Errorf("failed to get database: %w", err)
		}
		tokenCreateEnt, err = db.TokenCreate.Query().Where(tokencreate.TokenIdentifierEQ(req.FreezeTokensPayload.TokenIdentifier)).Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get single token for freeze request: %w", err)
		}
	} else {
		tokenCreateEnt, err = db.TokenCreate.Query().Where(tokencreate.IssuerPublicKeyEQ(req.FreezeTokensPayload.TokenPublicKey)).Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get single token for freeze request: %w", err)
		}
	}
	expectedIssuerPublicKey, err := keys.ParsePublicKey(tokenCreateEnt.IssuerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse expected issuer public key: %w", err)
	}
	if err := utils.ValidateOwnershipSignature(req.IssuerSignature, freezePayloadHash, expectedIssuerPublicKey); err != nil {
		return nil, fmt.Errorf("invalid issuer signature %s to freeze token with identifier %x with issuer public key %x: %w", req.IssuerSignature, req.FreezeTokensPayload.TokenIdentifier, expectedIssuerPublicKey, err)
	}

	// Check for existing freeze.
	ownerPubKey, err := keys.ParsePublicKey(req.FreezeTokensPayload.OwnerPublicKey)
	if err != nil {
		return nil, errors.InvalidUserInputErrorf("failed to parse owner public key: %w", err)
	}

	activeFreezes, err := ent.GetActiveFreezes(ctx, []keys.Public{ownerPubKey}, tokenCreateEnt.ID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToQueryTokenFreezeStatus, err)
	}
	if req.FreezeTokensPayload.ShouldUnfreeze {
		if len(activeFreezes) == 0 {
			return nil, fmt.Errorf("no active freezes found to thaw")
		}
		if len(activeFreezes) > 1 {
			return nil, fmt.Errorf(tokens.ErrMultipleActiveFreezes)
		}
		err = ent.ThawActiveFreeze(ctx, activeFreezes[0].ID, req.FreezeTokensPayload.IssuerProvidedTimestamp)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToUpdateTokenFreeze, err)
		}
	} else { // Freeze
		if len(activeFreezes) > 0 {
			return nil, fmt.Errorf(tokens.ErrAlreadyFrozen)
		}
		err = ent.ActivateFreeze(ctx,
			ownerPubKey,
			tokenCreateEnt.ID,
			req.IssuerSignature,
			req.FreezeTokensPayload.IssuerProvidedTimestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToCreateTokenFreeze, err)
		}
	}
	// Collect information about the frozen outputs.
	tokenNetwork, err := common.NetworkFromSchemaNetwork(tokenCreateEnt.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to get token network: %w", err)
	}
	outputIDs, totalAmount, err := ent.GetOwnedTokenOutputStats(ctx,
		[]keys.Public{ownerPubKey},
		tokenCreateEnt.TokenIdentifier,
		tokenNetwork,
	)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToGetOwnedOutputStats, err)
	}

	return &tokenpb.FreezeTokensResponse{
		ImpactedOutputIds:   outputIDs,
		ImpactedTokenAmount: totalAmount.Bytes(),
	}, nil
}
