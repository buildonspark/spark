package tokens

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
)

// spentAmountByteLen is the width of the uint128 big-endian spent-amount counter.
const spentAmountByteLen = 16

// defaultMaxActiveAllowancesPerOwner is the per-owner ACTIVE-allowance quota
// when the knob is unset.
const defaultMaxActiveAllowancesPerOwner = 100

// EnforceAllowanceCreateQuota rejects a create that would push the owner over
// its ACTIVE-allowance quota. Like the timestamp-freshness check it is enforced
// ONLY at the public coordinator edge, never on the internal replication path:
// replication converges operators on an already-admitted grant, and re-policing
// a count-based quota there could leave the fleet divergent (some peers accept,
// some reject, depending on replication order). Every user-reachable create
// goes through a public edge that runs this check.
//
// Concurrency: the owner's existing ACTIVE rows are locked FOR UPDATE (in
// allowance_id order, matching the release path's lock order) before counting,
// which bounds steady-state growth near the cap. An owner with no ACTIVE rows
// has nothing to lock, so concurrent first creates for distinct spender/token
// pairs can all pass and transiently exceed the cap. This is a soft DoS bound,
// not an invariant.
func EnforceAllowanceCreateQuota(ctx context.Context, ownerPublicKey keys.Public, payload *tokenpb.TokenAllowancePayload) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	if _, err := db.TokenAllowance.Query().
		Where(
			tokenallowance.OwnerPublicKey(ownerPublicKey),
			tokenallowance.StatusEQ(st.TokenAllowanceStatusActive),
		).
		Order(ent.Asc(tokenallowance.FieldAllowanceID)).
		ForUpdate().
		IDs(ctx); err != nil {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to lock owner allowances for quota check: %w", err))
	}
	activeCount, err := db.TokenAllowance.Query().
		Where(
			tokenallowance.OwnerPublicKey(ownerPublicKey),
			tokenallowance.StatusEQ(st.TokenAllowanceStatusActive),
		).
		Count(ctx)
	if err != nil {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to count owner allowances for quota check: %w", err))
	}
	maxAllowances := int(knobs.GetKnobsService(ctx).GetValue(knobs.KnobTokenMaxActiveAllowancesPerOwner, defaultMaxActiveAllowancesPerOwner))
	if activeCount >= maxAllowances {
		// An owner at the cap must still be able to retry a grant that was already
		// admitted: the retry repairs peers that missed the original, and rejecting
		// it would strand them permanently.
		identicalReplay, err := isIdenticalAllowanceCreate(ctx, payload)
		if err != nil {
			return err
		}
		if identicalReplay {
			return nil
		}
		return errors.ResourceExhaustedQuotaExceeded(fmt.Errorf("owner already has %d ACTIVE token allowances, the per-owner cap is %d; revoke an existing allowance first", activeCount, maxAllowances))
	}
	return nil
}

func isIdenticalAllowanceCreate(ctx context.Context, payload *tokenpb.TokenAllowancePayload) (bool, error) {
	statementHash, err := utils.HashCreateTokenAllowancePayload(payload)
	if err != nil {
		return false, errors.InternalUnhandledError(fmt.Errorf("failed to hash create token allowance payload: %w", err))
	}
	allowanceID, err := uuid.FromBytes(payload.GetAllowanceId())
	if err != nil {
		return false, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id: %w", err))
	}
	existing, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, errors.InternalDatabaseReadError(fmt.Errorf("failed to look up existing allowance %s: %w", allowanceID, err))
	}
	return bytes.Equal(existing.StatementHash, statementHash), nil
}

// ValidateAndApplyCreateAllowance validates an owner-signed allowance grant and installs it on
// this SO. It is shared between the coordinator (AllowanceTokenHandler) and the peer entry point
// (InternalCreateTokenAllowance); every operator runs the full validation independently and does
// not trust the coordinator's decision.
//
// Timestamp freshness is deliberately NOT enforced here. It is enforced only at the public
// coordinator edge: replication recovery replays the identical signed payload, and a peer that
// was down longer than the freshness window would otherwise reject the original timestamp
// forever, permanently stranding a partially-replicated grant. Replay is blocked structurally
// instead (unique allowance_id, statement-hash idempotency, permanent tombstones, monotonic
// revoke timestamps).
func ValidateAndApplyCreateAllowance(
	ctx context.Context,
	config *so.Config,
	payload *tokenpb.TokenAllowancePayload,
	ownerSignature []byte,
) error {
	if err := utils.ValidateTokenAllowancePayload(payload, config.SupportedNetworks); err != nil {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("token allowance payload validation failed: %w", err))
	}

	statementHash, err := utils.HashCreateTokenAllowancePayload(payload)
	if err != nil {
		return errors.InternalUnhandledError(fmt.Errorf("failed to hash create token allowance payload: %w", err))
	}

	ownerPublicKey, err := keys.ParsePublicKey(payload.GetOwnerPublicKey())
	if err != nil {
		return errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner public key: %w", err))
	}
	spenderPublicKey, err := keys.ParsePublicKey(payload.GetSpenderPublicKey())
	if err != nil {
		return errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse spender public key: %w", err))
	}
	if err := utils.ValidateOwnershipSignature(ownerSignature, statementHash, ownerPublicKey); err != nil {
		return errors.FailedPreconditionBadSignature(fmt.Errorf("invalid owner signature for token allowance %x: %w", payload.GetAllowanceId(), err))
	}

	allowanceID, err := uuid.FromBytes(payload.GetAllowanceId())
	if err != nil {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id: %w", err))
	}

	// Lock the token row so a concurrent create for the same token serializes here and the
	// (owner, spender, token) uniqueness check below cannot race two inserts through.
	tokenCreateEnt, err := ent.GetTokenCreateByIdentifierForUpdate(ctx, payload.GetTokenIdentifier())
	if err != nil {
		if ent.IsNotFound(err) {
			return errors.NotFoundMissingEntity(fmt.Errorf("no token exists for allowance request: %w", err))
		}
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to get token for allowance request: %w", err))
	}

	payloadNetwork, err := btcnetwork.FromProtoNetwork(payload.GetNetwork())
	if err != nil {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert allowance network: %w", err))
	}
	if payloadNetwork != tokenCreateEnt.Network {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("allowance network %s does not match token network %s", payloadNetwork, tokenCreateEnt.Network))
	}

	existing, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	if err != nil && !ent.IsNotFound(err) {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to look up existing allowance %s: %w", allowanceID, err))
	}
	if existing != nil {
		// Tombstones never resurrect: a revoked allowance_id can never be re-created.
		if existing.Status == st.TokenAllowanceStatusRevoked {
			return errors.FailedPreconditionInvalidState(fmt.Errorf("allowance %s was revoked and cannot be recreated", allowanceID))
		}
		// Idempotent replay of the identical signed grant.
		if bytes.Equal(existing.StatementHash, statementHash) {
			return nil
		}
		return errors.FailedPreconditionInvalidState(fmt.Errorf("allowance %s already exists with a different statement hash", allowanceID))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	expirySeconds := payload.GetExpiryTime().AsTime().Unix()

	create := db.TokenAllowance.Create().
		SetAllowanceID(allowanceID).
		SetStatus(st.TokenAllowanceStatusActive).
		SetOwnerPublicKey(ownerPublicKey).
		SetSpenderPublicKey(spenderPublicKey).
		SetTokenIdentifier(payload.GetTokenIdentifier()).
		SetTokenCreateID(tokenCreateEnt.ID).
		SetPerTransactionCap(payload.GetPerTransactionCap()).
		SetTotalLimit(payload.GetTotalLimit()).
		SetSpentAmount(make([]byte, spentAmountByteLen)).
		SetRecipientAllowlist(payload.GetRecipientAllowlist()).
		SetExpiryTime(time.Unix(expirySeconds, 0)).
		SetNetwork(payloadNetwork).
		SetOwnerSignature(ownerSignature).
		SetStatementHash(statementHash).
		SetVersion(uint64(payload.GetVersion())).
		SetOwnerProvidedTimestamp(payload.GetOwnerProvidedTimestamp())

	if _, err := create.Save(ctx); err != nil {
		// The partial-unique index (one ACTIVE grant per owner/spender/token) and the unique
		// allowance_id both surface here as constraint violations; report them cleanly.
		if ent.IsConstraintError(err) {
			return duplicateAllowanceCreateError(allowanceID, err)
		}
		return errors.InternalDatabaseWriteError(fmt.Errorf("failed to create token allowance %s: %w", allowanceID, err))
	}

	return nil
}

func duplicateAllowanceCreateError(allowanceID uuid.UUID, err error) error {
	switch {
	case strings.Contains(err.Error(), "token_allowances_allowance_id_key"):
		return errors.AlreadyExistsDuplicateOperation(fmt.Errorf("allowance ID %s already exists: %w", allowanceID, err))
	case strings.Contains(err.Error(), "tokenallowance_unique_active_grant"):
		return errors.AlreadyExistsDuplicateOperation(fmt.Errorf("an active allowance already exists for this owner, spender, and token: %w", err))
	default:
		return errors.AlreadyExistsDuplicateOperation(fmt.Errorf("token allowance violates a uniqueness constraint: %w", err))
	}
}
