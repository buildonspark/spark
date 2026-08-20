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
	"github.com/lightsparkdev/spark/common/logging"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
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

// validateAllowanceCreatePreflight rejects invalid public requests before 2PC
// without taking locks. BuildCommitPayload repeats the state-dependent checks
// with locks so concurrent changes are still enforced authoritatively.
func validateAllowanceCreatePreflight(
	ctx context.Context,
	config *so.Config,
	ownerPublicKey keys.Public,
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
	if err := utils.ValidateOwnershipSignature(ownerSignature, statementHash, ownerPublicKey); err != nil {
		return errors.FailedPreconditionBadSignature(fmt.Errorf("invalid owner signature for token allowance %x: %w", payload.GetAllowanceId(), err))
	}

	tokenCreateEnt, err := ent.GetTokenCreateByIdentifier(ctx, payload.GetTokenIdentifier())
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

	allowanceID, err := uuid.FromBytes(payload.GetAllowanceId())
	if err != nil {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id: %w", err))
	}
	existing, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	if err != nil && !ent.IsNotFound(err) {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to look up existing allowance %s: %w", allowanceID, err))
	}
	if existing != nil {
		if existing.Status == st.TokenAllowanceStatusRevoked {
			return errors.FailedPreconditionInvalidState(fmt.Errorf("allowance %s was revoked and cannot be recreated", allowanceID))
		}
		if bytes.Equal(existing.StatementHash, statementHash) {
			return nil
		}
		return errors.FailedPreconditionInvalidState(fmt.Errorf("allowance %s already exists with a different statement hash", allowanceID))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	spenderPublicKey, err := keys.ParsePublicKey(payload.GetSpenderPublicKey())
	if err != nil {
		return errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse spender public key: %w", err))
	}
	activeGrantExists, err := db.TokenAllowance.Query().
		Where(
			tokenallowance.OwnerPublicKey(ownerPublicKey),
			tokenallowance.SpenderPublicKey(spenderPublicKey),
			tokenallowance.TokenCreateID(tokenCreateEnt.ID),
			tokenallowance.StatusEQ(st.TokenAllowanceStatusActive),
		).
		Exist(ctx)
	if err != nil {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to check for an existing active allowance: %w", err))
	}
	if activeGrantExists {
		return errors.AlreadyExistsDuplicateOperation(fmt.Errorf("an active allowance already exists for this owner, spender, and token"))
	}
	activeCount, err := db.TokenAllowance.Query().
		Where(
			tokenallowance.OwnerPublicKey(ownerPublicKey),
			tokenallowance.StatusEQ(st.TokenAllowanceStatusActive),
		).
		Count(ctx)
	if err != nil {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to count owner allowances for quota preflight: %w", err))
	}
	maxAllowances := int(knobs.GetKnobsService(ctx).GetValue(knobs.KnobTokenMaxActiveAllowancesPerOwner, defaultMaxActiveAllowancesPerOwner))
	if activeCount >= maxAllowances {
		return errors.ResourceExhaustedQuotaExceeded(fmt.Errorf("owner already has %d ACTIVE token allowances, the per-owner cap is %d; revoke an existing allowance first", activeCount, maxAllowances))
	}
	return nil
}

// EnforceAllowanceCreateQuota rejects a create that would push the owner over
// its ACTIVE-allowance quota. Like the timestamp-freshness check it is enforced
// ONLY as coordinator admission, never on participant Prepare: participants
// converge on an already-admitted grant, and re-policing a count-based quota
// there could make votes depend on each operator's replication history. Every
// user-reachable create runs this check before the coordinator writes its row.
//
// Concurrency: the owner's existing ACTIVE rows are locked FOR UPDATE (in
// allowance_id order, matching the release path's lock order) before counting,
// and the coordinator inserts in that same transaction. An owner with no ACTIVE
// rows has nothing to lock, so concurrent first creates for distinct
// spender/token pairs can all pass and transiently exceed the cap. This is a
// soft DoS bound, not an invariant.
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

// isIdenticalAllowanceCreate reports whether the allowance ID already stores
// the exact owner-signed statement in payload.
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
// this SO. It is shared by coordinator-local and participant Prepare work; every operator runs
// the full validation independently and does not trust the coordinator's decision.
//
// Timestamp freshness (the symmetric window) is deliberately NOT enforced here; only the
// future bound above is. The freshness window is enforced at the public
// coordinator edge: participant recovery replays the identical signed payload, and a peer that
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
	// Bounded on every operator, not just the coordinator edge: a future-dated grant can never
	// be revoked, because a revoke that predates the grant timestamp is refused as stale.
	if err := ValidateTimestampNotFutureDated(payload.GetOwnerProvidedTimestamp()); err != nil {
		return err
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
			return validateAllowanceCreateAdoption(ctx, allowanceID, existing.FlowExecutionID)
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
		SetPerTransactionUnlimited(payload.GetPerTransactionUnlimited()).
		SetTotalUnlimited(payload.GetTotalUnlimited()).
		SetSpentAmount(make([]byte, spentAmountByteLen)).
		SetRecipientAllowlist(payload.GetRecipientAllowlist()).
		SetExpiryTime(time.Unix(expirySeconds, 0)).
		SetNetwork(payloadNetwork).
		SetOwnerSignature(ownerSignature).
		SetStatementHash(statementHash).
		SetVersion(uint64(payload.GetVersion())).
		SetOwnerProvidedTimestamp(payload.GetOwnerProvidedTimestamp())
	if flowExecutionID, ok := consensus.FlowExecutionIDFromContext(ctx); ok {
		create.SetFlowExecutionID(flowExecutionID)
	}

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

// validateAllowanceCreateAdoption prevents one consensus flow from claiming a
// row that a different in-flight flow may still roll back.
func validateAllowanceCreateAdoption(ctx context.Context, allowanceID uuid.UUID, ownerFlowID *uuid.UUID) error {
	if ownerFlowID == nil {
		return nil
	}
	if currentFlowID, ok := consensus.FlowExecutionIDFromContext(ctx); ok && currentFlowID == *ownerFlowID {
		return nil
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	ownerFlow, err := db.FlowExecution.Query().
		Where(flowexecution.ID(*ownerFlowID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		// Participant flow rows are purged only after reaching a terminal state.
		// A surviving allowance therefore cannot still be removed by its owner.
		return nil
	}
	if err != nil {
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to inspect owning flow %s for allowance %s: %w", ownerFlowID, allowanceID, err))
	}
	switch ownerFlow.Status {
	case st.FlowExecutionStatusCommitted:
		return nil
	case st.FlowExecutionStatusRolledBack:
		// Rollback deletes the allowance before the flow becomes terminal. A
		// surviving row indicates inconsistent state and must not be adopted as
		// though the grant had committed.
		return errors.FailedPreconditionInvalidState(fmt.Errorf("allowance %s is owned by rolled-back consensus flow %s", allowanceID, ownerFlowID))
	case st.FlowExecutionStatusInFlight:
		// Flow status is monotonic, so a concurrent terminal transition can only
		// turn this rejection into a safe retry. Avoiding a row lock here also
		// preserves rollback's flow-row-before-allowance-row lock order.
		return errors.AbortedLockConflict(fmt.Errorf("allowance %s is still owned by consensus flow %s", allowanceID, ownerFlowID))
	default:
		return errors.InternalUnhandledError(fmt.Errorf("allowance %s is owned by consensus flow %s with unknown status %q", allowanceID, ownerFlowID, ownerFlow.Status))
	}
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

// ValidateAndApplyRevokeAllowance verifies an owner-signed revocation and tombstones the allowance
// on this SO. Rows are never deleted; the tombstone is the replay protection. Shared between the
// coordinator and the peer entry point, with each operator validating independently.
//
// Like ValidateAndApplyCreateAllowance, the symmetric freshness window is enforced only at the public
// coordinator edge so replication recovery can replay the original signed payload indefinitely.
func ValidateAndApplyRevokeAllowance(
	ctx context.Context,
	config *so.Config,
	revokePayload *tokenpb.RevokeTokenAllowancePayload,
	ownerSignature []byte,
) error {
	if err := utils.ValidateRevokeTokenAllowancePayload(revokePayload); err != nil {
		return err
	}
	// Bounded on every operator for the same reason as create: a future-dated tombstone would
	// win the proof comparison against every later honest revoke.
	if err := ValidateTimestampNotFutureDated(revokePayload.GetOwnerProvidedTimestamp()); err != nil {
		return err
	}

	revokeTimestamp := revokePayload.GetOwnerProvidedTimestamp()

	statementHash, err := utils.HashRevokeTokenAllowancePayload(revokePayload)
	if err != nil {
		return errors.InternalUnhandledError(fmt.Errorf("failed to hash revoke token allowance payload: %w", err))
	}

	allowanceID, err := uuid.FromBytes(revokePayload.GetAllowanceId())
	if err != nil {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id: %w", err))
	}

	stored, err := ent.GetAllowanceByAllowanceIDForUpdate(ctx, allowanceID)
	if err != nil {
		if ent.IsNotFound(err) {
			return errors.NotFoundMissingEntity(fmt.Errorf("allowance %s not found", allowanceID))
		}
		return errors.InternalDatabaseReadError(fmt.Errorf("failed to load allowance %s: %w", allowanceID, err))
	}

	payloadOwnerPublicKey, err := keys.ParsePublicKey(revokePayload.GetOwnerPublicKey())
	if err != nil {
		return errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse revoke owner public key: %w", err))
	}
	if !payloadOwnerPublicKey.Equals(stored.OwnerPublicKey) {
		return errors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("revoke owner public key does not match the stored allowance owner public key"))
	}

	if err := utils.ValidateOwnershipSignature(ownerSignature, statementHash, stored.OwnerPublicKey); err != nil {
		return errors.FailedPreconditionBadSignature(fmt.Errorf("invalid owner signature to revoke allowance %s: %w", allowanceID, err))
	}

	if revokeTimestamp < stored.OwnerProvidedTimestamp {
		return errors.FailedPreconditionReplay(fmt.Errorf(
			"stale revoke request: revoke timestamp %d predates grant timestamp %d",
			revokeTimestamp, stored.OwnerProvidedTimestamp,
		))
	}

	supersedingTombstone := false
	if stored.Status == st.TokenAllowanceStatusRevoked {
		if stored.RevokeVersion == uint64(revokePayload.GetVersion()) &&
			stored.OwnerProvidedRevokeTimestamp == revokeTimestamp &&
			bytes.Equal(stored.RevokeSignature, ownerSignature) {
			return nil
		}
		if !revokeProofPrecedes(
			revokeTimestamp,
			ownerSignature,
			stored.OwnerProvidedRevokeTimestamp,
			stored.RevokeSignature,
		) {
			logging.GetLoggerFromContext(ctx).Sugar().Infof(
				"token allowance %s dropped a non-canonical revoke proof with timestamp %d",
				allowanceID,
				revokeTimestamp,
			)
			return nil
		}
		supersedingTombstone = true
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	if _, err := db.TokenAllowance.UpdateOneID(stored.ID).
		SetStatus(st.TokenAllowanceStatusRevoked).
		SetOwnerProvidedRevokeTimestamp(revokeTimestamp).
		SetRevokeSignature(ownerSignature).
		// Persist the revoke payload's version so the tombstoned grant can be
		// served back with a verifiable owner-signed revoke proof.
		SetRevokeVersion(uint64(revokePayload.GetVersion())).
		Save(ctx); err != nil {
		return errors.InternalDatabaseWriteError(fmt.Errorf("failed to revoke token allowance %s: %w", allowanceID, err))
	}
	if supersedingTombstone {
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"token allowance %s revoke tombstone superseded by canonical proof with timestamp %d",
			allowanceID,
			revokeTimestamp,
		)
	}

	return nil
}

// revokeProofPrecedes makes every operator select the same tombstone regardless of proof arrival order.
func revokeProofPrecedes(incomingTimestamp uint64, incomingSignature []byte, storedTimestamp uint64, storedSignature []byte) bool {
	if incomingTimestamp != storedTimestamp {
		return incomingTimestamp < storedTimestamp
	}
	return bytes.Compare(incomingSignature, storedSignature) < 0
}
