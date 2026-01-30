package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
)

// FreezeResult contains the result of a freeze operation.
type FreezeResult struct {
	OutputRefs  []*tokenpb.TokenOutputRef
	TotalAmount []byte
}

// GetIssuerPublicKeyForFreeze looks up the token by identifier and returns the issuer public key.
// This is used for session auth validation before processing a freeze request.
func GetIssuerPublicKeyForFreeze(ctx context.Context, tokenIdentifier []byte) (*keys.Public, error) {
	if tokenIdentifier == nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("token identifier is required"))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to get database: %w", err))
	}

	tokenCreateEnt, err := db.TokenCreate.Query().Where(tokencreate.TokenIdentifier(tokenIdentifier)).Only(ctx)
	if err != nil {
		return nil, errors.NotFoundMissingEntity(fmt.Errorf("failed to get token for freeze request: %w", err))
	}

	return &tokenCreateEnt.IssuerPublicKey, nil
}

// ValidateAndApplyFreeze validates a freeze request and applies the freeze/unfreeze operation.
// This is shared between the external FreezeTokenHandler and InternalFreezeTokenHandler.
func ValidateAndApplyFreeze(
	ctx context.Context,
	config *so.Config,
	freezePayload *tokenpb.FreezeTokensPayload,
	issuerSignature []byte,
) (*FreezeResult, error) {
	// 1. Validate freeze tokens payload
	if err := utils.ValidateFreezeTokensPayload(freezePayload, config.IdentityPublicKey()); err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("freeze tokens payload validation failed: %w", err))
	}

	freezePayloadHash, err := utils.HashFreezeTokensPayload(freezePayload)
	if err != nil {
		return nil, errors.InternalUnhandledError(fmt.Errorf("failed to hash freeze tokens payload: %w", err))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to get database: %w", err))
	}

	// 2. Query token and verify it exists
	var tokenCreateEnt *ent.TokenCreate
	if freezeTokenID := freezePayload.GetTokenIdentifier(); freezeTokenID != nil {
		tokenCreateEnt, err = db.TokenCreate.Query().Where(tokencreate.TokenIdentifier(freezeTokenID)).Only(ctx)
		if err != nil {
			return nil, errors.NotFoundMissingEntity(fmt.Errorf("failed to get single token for freeze request: %w", err))
		}
	}

	// 3. Verify token is freezable
	if !tokenCreateEnt.IsFreezable {
		return nil, errors.FailedPreconditionTokenRulesViolation(fmt.Errorf("%s: token identifier %x", tokens.ErrTokenNotFreezable, tokenCreateEnt.TokenIdentifier))
	}

	// 4. Validate issuer signature against the token's issuer_public_key from DB
	expectedIssuerPublicKey := tokenCreateEnt.IssuerPublicKey
	if err := utils.ValidateOwnershipSignature(issuerSignature, freezePayloadHash, expectedIssuerPublicKey); err != nil {
		return nil, errors.FailedPreconditionBadSignature(fmt.Errorf("invalid issuer signature to freeze token with identifier %x: %w", freezePayload.GetTokenIdentifier(), err))
	}

	// 5. Parse owner public key
	ownerPubKey, err := keys.ParsePublicKey(freezePayload.OwnerPublicKey)
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner public key: %w", err))
	}

	// 6. Check for existing freeze and handle with timestamp-based ordering
	activeFreezes, err := ent.GetActiveFreezes(ctx, []keys.Public{ownerPubKey}, tokenCreateEnt.ID)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("%s: %w", tokens.ErrFailedToQueryTokenFreezeStatus, err))
	}

	if freezePayload.ShouldUnfreeze {
		if len(activeFreezes) == 0 {
			// Already unfrozen - but check if this is a stale request
			// (a more recent freeze may have happened after this unfreeze was issued)
			return buildFreezeResult(ctx, ownerPubKey, tokenCreateEnt)
		}
		if len(activeFreezes) > 1 {
			return nil, errors.FailedPreconditionInvalidState(fmt.Errorf(tokens.ErrMultipleActiveFreezes))
		}
		// Reject stale unfreeze: if the active freeze is newer than this unfreeze request
		activeFreeze := activeFreezes[0]
		if activeFreeze.WalletProvidedFreezeTimestamp > freezePayload.IssuerProvidedTimestamp {
			return nil, errors.FailedPreconditionReplay(fmt.Errorf(
				"stale unfreeze request: active freeze timestamp %d is newer than unfreeze timestamp %d",
				activeFreeze.WalletProvidedFreezeTimestamp, freezePayload.IssuerProvidedTimestamp,
			))
		}
		err = ent.ThawActiveFreeze(ctx, activeFreeze.ID, freezePayload.IssuerProvidedTimestamp)
		if err != nil {
			return nil, errors.InternalDatabaseWriteError(fmt.Errorf("%s: %w", tokens.ErrFailedToUpdateTokenFreeze, err))
		}
	} else {
		// Freeze
		if len(activeFreezes) > 0 {
			// Already frozen - but check if this is a stale request being replayed
			activeFreeze := activeFreezes[0]
			if activeFreeze.WalletProvidedFreezeTimestamp >= freezePayload.IssuerProvidedTimestamp {
				// Same or older timestamp means idempotent or stale - return success without re-freezing
				return buildFreezeResult(ctx, ownerPubKey, tokenCreateEnt)
			}
			// Newer freeze request on already frozen token - this shouldn't normally happen
			// but we'll allow it as it doesn't change the frozen state
			return buildFreezeResult(ctx, ownerPubKey, tokenCreateEnt)
		}
		// Check for replay: reject if there's a more recent thaw
		mostRecentThaw, err := ent.GetMostRecentThawTimestamp(ctx, ownerPubKey, tokenCreateEnt.ID)
		if err != nil {
			return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to check for recent thaw: %w", err))
		}
		if mostRecentThaw > freezePayload.IssuerProvidedTimestamp {
			return nil, errors.FailedPreconditionReplay(fmt.Errorf(
				"stale freeze request: most recent thaw timestamp %d is newer than freeze timestamp %d",
				mostRecentThaw, freezePayload.IssuerProvidedTimestamp,
			))
		}
		err = ent.ActivateFreeze(ctx,
			ownerPubKey,
			tokenCreateEnt.ID,
			issuerSignature,
			freezePayload.IssuerProvidedTimestamp,
		)
		if err != nil {
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
