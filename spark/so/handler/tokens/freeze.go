package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
)

// FreezeResult contains the result of a freeze operation.
type FreezeResult struct {
	OutputRefs  []*tokenpb.TokenOutputRef
	TotalAmount []byte
}

// validateFreezeState checks whether a freeze/unfreeze operation should proceed, is idempotent,
// or should be rejected. Returns idempotent=true if the request was already applied (caller should
// return success). Returns a non-nil error if the request is invalid.
func validateFreezeState(
	active *ent.TokenFreeze,
	mostRecentThawTimestamp uint64,
	shouldUnfreeze bool,
	requestTimestamp uint64,
) (idempotent bool, err error) {
	if shouldUnfreeze {
		if active == nil {
			if mostRecentThawTimestamp == requestTimestamp {
				return true, nil
			}
			if mostRecentThawTimestamp > 0 {
				return false, errors.FailedPreconditionInvalidState(fmt.Errorf(
					"token already unfrozen with timestamp %d, cannot unfreeze with different timestamp %d",
					mostRecentThawTimestamp, requestTimestamp,
				))
			}
			return true, nil
		}
		if active.WalletProvidedFreezeTimestamp > requestTimestamp {
			return false, errors.FailedPreconditionReplay(fmt.Errorf(
				"stale unfreeze request: active freeze timestamp %d is newer than unfreeze timestamp %d",
				active.WalletProvidedFreezeTimestamp, requestTimestamp,
			))
		}
		return false, nil
	}

	// Freeze path.
	if active != nil {
		if active.WalletProvidedFreezeTimestamp == requestTimestamp {
			return true, nil
		}
		return false, errors.FailedPreconditionInvalidState(fmt.Errorf(
			"token already frozen with timestamp %d, cannot freeze with different timestamp %d",
			active.WalletProvidedFreezeTimestamp, requestTimestamp,
		))
	}
	if mostRecentThawTimestamp > requestTimestamp {
		return false, errors.FailedPreconditionReplay(fmt.Errorf(
			"stale freeze request: most recent unfreeze timestamp %d is newer than freeze timestamp %d",
			mostRecentThawTimestamp, requestTimestamp,
		))
	}
	return false, nil
}

// ValidateAndApplyFreeze validates a freeze request and applies the freeze/unfreeze operation.
// This is shared between the external FreezeTokenHandler and InternalFreezeTokenHandler.
func ValidateAndApplyFreeze(
	ctx context.Context,
	config *so.Config,
	freezePayload *tokenpb.FreezeTokensPayload,
	issuerSignature []byte,
) (*FreezeResult, error) {
	if err := utils.ValidateFreezeTokensPayload(freezePayload, config.IdentityPublicKey()); err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("freeze tokens payload validation failed: %w", err))
	}

	if err := ValidateTimestampMillis(freezePayload.IssuerProvidedTimestamp); err != nil {
		return nil, err
	}

	freezePayloadHash, err := utils.HashFreezeTokensPayload(freezePayload)
	if err != nil {
		return nil, errors.InternalUnhandledError(fmt.Errorf("failed to hash freeze tokens payload: %w", err))
	}

	// Lock the TokenCreate row to prevent concurrent freeze/unfreeze race conditions.
	tokenCreateEnt, err := ent.GetTokenCreateByIdentifierForUpdate(ctx, freezePayload.GetTokenIdentifier())
	if err != nil {
		return nil, errors.NotFoundMissingEntity(fmt.Errorf("failed to get token for freeze request: %w", err))
	}

	if !tokenCreateEnt.IsFreezable {
		return nil, errors.FailedPreconditionTokenRulesViolation(fmt.Errorf("%s: token identifier %x", tokens.ErrTokenNotFreezable, tokenCreateEnt.TokenIdentifier))
	}

	expectedIssuerPublicKey := tokenCreateEnt.IssuerPublicKey
	if err := utils.ValidateOwnershipSignature(issuerSignature, freezePayloadHash, expectedIssuerPublicKey); err != nil {
		return nil, errors.FailedPreconditionBadSignature(fmt.Errorf("invalid issuer signature to freeze token with identifier %x: %w", freezePayload.GetTokenIdentifier(), err))
	}

	isGlobalPause := len(freezePayload.GetOwnerPublicKey()) == 0
	if isGlobalPause {
		return applyGlobalPause(ctx, freezePayload, issuerSignature, tokenCreateEnt)
	}
	return applyPerOwnerFreeze(ctx, freezePayload, issuerSignature, tokenCreateEnt)
}

func applyGlobalPause(
	ctx context.Context,
	freezePayload *tokenpb.FreezeTokensPayload,
	issuerSignature []byte,
	tokenCreateEnt *ent.TokenCreate,
) (*FreezeResult, error) {
	activePause, err := ent.GetActiveGlobalPause(ctx, tokenCreateEnt.ID)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("%s: %w", tokens.ErrFailedToQueryTokenFreezeStatus, err))
	}

	mostRecentThaw, err := ent.GetMostRecentGlobalThawTimestamp(ctx, tokenCreateEnt.ID)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to check for recent global thaw: %w", err))
	}

	idempotent, err := validateFreezeState(activePause, mostRecentThaw, freezePayload.ShouldUnfreeze, freezePayload.IssuerProvidedTimestamp)
	if err != nil {
		return nil, err
	}
	if idempotent {
		return &FreezeResult{}, nil
	}

	if freezePayload.ShouldUnfreeze {
		if activePause == nil {
			return nil, errors.InternalDataInconsistency(fmt.Errorf("expected active global pause but found none for token_create_id %s", tokenCreateEnt.ID))
		}
		if err := ent.ThawActiveFreeze(ctx, activePause.ID, freezePayload.IssuerProvidedTimestamp); err != nil {
			return nil, errors.InternalDatabaseWriteError(fmt.Errorf("%s: %w", tokens.ErrFailedToUpdateTokenFreeze, err))
		}
	} else {
		if err := ent.ActivateGlobalPause(ctx, tokenCreateEnt.ID, issuerSignature, freezePayload.IssuerProvidedTimestamp); err != nil {
			return nil, errors.InternalDatabaseWriteError(fmt.Errorf("%s: %w", tokens.ErrFailedToCreateTokenFreeze, err))
		}
	}

	// Global pause affects all owners, so we don't return per-owner output refs or amounts.
	return &FreezeResult{}, nil
}

func applyPerOwnerFreeze(
	ctx context.Context,
	freezePayload *tokenpb.FreezeTokensPayload,
	issuerSignature []byte,
	tokenCreateEnt *ent.TokenCreate,
) (*FreezeResult, error) {
	ownerPubKey, err := keys.ParsePublicKey(freezePayload.OwnerPublicKey)
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner public key: %w", err))
	}

	activeFreezes, err := ent.GetActiveFreezes(ctx, []keys.Public{ownerPubKey}, tokenCreateEnt.ID)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("%s: %w", tokens.ErrFailedToQueryTokenFreezeStatus, err))
	}
	if len(activeFreezes) > 1 {
		return nil, errors.InternalDataInconsistency(fmt.Errorf(tokens.ErrMultipleActiveFreezes))
	}

	var activeFreeze *ent.TokenFreeze
	if len(activeFreezes) == 1 {
		activeFreeze = activeFreezes[0]
	}

	mostRecentThaw, err := ent.GetMostRecentThawTimestamp(ctx, ownerPubKey, tokenCreateEnt.ID)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to check for recent thaw: %w", err))
	}

	idempotent, err := validateFreezeState(activeFreeze, mostRecentThaw, freezePayload.ShouldUnfreeze, freezePayload.IssuerProvidedTimestamp)
	if err != nil {
		return nil, err
	}
	if idempotent {
		return buildFreezeResult(ctx, ownerPubKey, tokenCreateEnt)
	}

	if freezePayload.ShouldUnfreeze {
		if activeFreeze == nil {
			return nil, errors.InternalDataInconsistency(fmt.Errorf("expected active freeze but found none for owner %x and token_create_id %s", freezePayload.GetOwnerPublicKey(), tokenCreateEnt.ID))
		}
		if err := ent.ThawActiveFreeze(ctx, activeFreeze.ID, freezePayload.IssuerProvidedTimestamp); err != nil {
			return nil, errors.InternalDatabaseWriteError(fmt.Errorf("%s: %w", tokens.ErrFailedToUpdateTokenFreeze, err))
		}
	} else {
		if err := ent.ActivateFreeze(ctx, ownerPubKey, tokenCreateEnt.ID, issuerSignature, freezePayload.IssuerProvidedTimestamp); err != nil {
			return nil, errors.InternalDatabaseWriteError(fmt.Errorf("%s: %w", tokens.ErrFailedToCreateTokenFreeze, err))
		}
	}

	return buildFreezeResult(ctx, ownerPubKey, tokenCreateEnt)
}

func buildFreezeResult(
	ctx context.Context,
	ownerPubKey keys.Public,
	tokenCreateEnt *ent.TokenCreate,
) (*FreezeResult, error) {
	result, err := ent.GetOwnedTokenOutputRefs(ctx,
		[]keys.Public{ownerPubKey},
		tokenCreateEnt.TokenIdentifier,
		tokenCreateEnt.Network,
	)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("%s: %w", tokens.ErrFailedToGetOwnedOutputStats, err))
	}

	return &FreezeResult{
		OutputRefs:  result.OutputRefs,
		TotalAmount: result.TotalAmount.Bytes(),
	}, nil
}
