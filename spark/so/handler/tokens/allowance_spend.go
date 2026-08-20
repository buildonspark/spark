package tokens

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/ent/tokenallowancespend"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
)

// maxUint128 is the ceiling of the spent_amount meter column (uint128 big-endian).
var maxUint128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

// extractAllowanceAuthorization inspects a transfer's input signatures and returns the allowance
// ID they cite, or nil when the transfer is owner-signed. V1 policy: a transaction is either
// fully owner-signed or fully allowance-authorized under a single allowance; mixing modes or
// citing different allowance IDs across inputs is rejected.
func extractAllowanceAuthorization(sigs []*tokenpb.SignatureWithIndex) (*uuid.UUID, error) {
	var allowanceID *uuid.UUID
	allowanceArmCount := 0
	for _, sig := range sigs {
		arm, ok := sig.GetAuthoritySignatures().(*tokenpb.SignatureWithIndex_AllowanceSignature)
		if !ok {
			continue
		}
		allowanceArmCount++
		id, err := uuid.FromBytes(arm.AllowanceSignature.GetAllowanceId())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id in input signature: %w", err))
		}
		if allowanceID == nil {
			allowanceID = &id
		} else if *allowanceID != id {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"%s: inputs cite allowances %s and %s", tokens.ErrAllowanceMixedAuthorization, *allowanceID, id))
		}
	}
	if allowanceArmCount == 0 {
		return nil, nil
	}
	if allowanceArmCount != len(sigs) {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
			"%s: %d of %d inputs use the allowance arm", tokens.ErrAllowanceMixedAuthorization, allowanceArmCount, len(sigs)))
	}
	return allowanceID, nil
}

// validateAllowanceSignature verifies a delegated spender signature over hash. It mirrors the
// single-signature owner arm: the embedded public key must equal the spender key recorded on the
// grant (never a key claimed by the request), and the signature must verify against that key.
func validateAllowanceSignature(_ context.Context, sig *tokenpb.AllowanceSignature, hash []byte, allowance *ent.TokenAllowance) error {
	if sig == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("allowance signature cannot be nil"))
	}
	sigAllowanceID, err := uuid.FromBytes(sig.GetAllowanceId())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid allowance_id in allowance signature: %w", err))
	}
	if sigAllowanceID != allowance.AllowanceID {
		return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf(
			"allowance signature cites allowance %s but allowance %s is loaded for this transaction", sigAllowanceID, allowance.AllowanceID))
	}
	keyedSig := sig.GetSpenderSignature()
	if keyedSig == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("allowance signature must include a spender_signature"))
	}
	pubKeyBytes := keyedSig.GetPublicKey()
	if len(pubKeyBytes) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("spender_signature must include a public key"))
	}
	claimedKey, err := keys.ParsePublicKey(pubKeyBytes)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid public key in spender_signature: %w", err))
	}
	if !claimedKey.Equals(allowance.SpenderPublicKey) {
		return sparkerrors.FailedPreconditionBadSignature(
			fmt.Errorf("spender_signature public key does not match the allowance spender"))
	}
	return utils.ValidateOwnershipSignature(keyedSig.GetSignature(), hash, allowance.SpenderPublicKey)
}

// validateAndMeterAllowanceSpend runs the transaction-level allowance policy checks and reserves
// budget for one delegated transfer. It is called exactly once per prepare, after the started
// transaction entities exist, inside the same database transaction, so a policy failure rolls
// back the entire prepare and no operator ever holds a prepared-but-unmetered delegated spend.
func validateAndMeterAllowanceSpend(
	ctx context.Context,
	_ *so.Config,
	finalTokenTx *tokenpb.TokenTransaction,
	allowanceID uuid.UUID,
	tokenTxEnt *ent.TokenTransaction,
	inputTtxos []*ent.TokenOutput,
) error {
	if !allowancesEnabled(ctx) {
		return sparkerrors.UnimplementedMethodDisabled(fmt.Errorf("token allowances are not enabled"))
	}
	if finalTokenTx.GetVersion() < 3 {
		return sparkerrors.InvalidArgumentInvalidVersion(fmt.Errorf(
			"allowance-authorized transfers require token transaction version 3+, got %d", finalTokenTx.GetVersion()))
	}

	// LOCK ORDER: the prepare flow locks the input token outputs (FetchAndLockTokenInputs)
	// before this runs; the allowance row is locked after the TTXOs, and the allowance's spend
	// rows are locked last. A transaction cites exactly one allowance (enforced by
	// extractAllowanceAuthorization), so metering never locks two different allowances in one
	// database transaction.
	allowance, err := ent.GetAllowanceByAllowanceIDForUpdate(ctx, allowanceID)
	if err != nil {
		if ent.IsNotFound(err) {
			return sparkerrors.NotFoundMissingEntity(fmt.Errorf("%s: %s", tokens.ErrAllowanceNotFound, allowanceID))
		}
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to lock allowance %s: %w", allowanceID, err))
	}

	// EXHAUSTED is spendable here: lazy release below may free budget and flip it back to ACTIVE.
	if err := checkAllowanceSpendable(allowance); err != nil {
		return err
	}
	txNetwork, err := btcnetwork.FromProtoNetwork(finalTokenTx.GetNetwork())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert transaction network: %w", err))
	}
	if txNetwork != allowance.Network {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"transaction network %s does not match allowance network %s", txNetwork, allowance.Network))
	}
	for i, input := range inputTtxos {
		if !input.OwnerPublicKey.Equals(allowance.OwnerPublicKey) {
			return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"input %d owner does not match the allowance owner", i))
		}
		if input.TokenCreateID != allowance.TokenCreateID {
			return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"input %d token does not match the allowance token", i))
		}
	}

	spent := new(big.Int).SetBytes(allowance.SpentAmount)

	// Lazy release: return budget held by spends whose transactions can no longer finalize
	// (cancelled, or STARTED past expiry). This is what lets an EXHAUSTED allowance flip back
	// to ACTIVE and is idempotent because only RESERVED rows are flipped.
	//
	// The spend-row FOR UPDATE lock below is what makes the expiry decision safe against an
	// in-flight finalization: the signing gate (validateAllowanceSpendReservedForSigning) locks
	// the spend row from its pre-expiry check until its transaction commits, so this query
	// either blocks until that commit (and the eager-loaded transaction status then shows
	// SIGNED/REVEALED/FINALIZED - not releasable), or acquires the lock first, releases the
	// spend, and the late finalizer is rejected at the gate because the spend is RELEASED.
	staleSpends, err := ent.GetReservedSpendsForAllowanceForUpdate(ctx, allowance.AllowanceID)
	if err != nil {
		return err
	}
	releasableSpends := make([]*ent.TokenAllowanceSpend, 0, len(staleSpends))
	for _, spend := range staleSpends {
		spendTx := spend.Edges.TokenTransaction
		if spendTx == nil {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf(
				"allowance spend %s has no token transaction edge", spend.ID))
		}
		if reservedAllowanceSpendIsReleasable(spendTx) {
			releasableSpends = append(releasableSpends, spend)
		}
	}
	if err := releaseReservedSpendRows(ctx, releasableSpends, spent); err != nil {
		return err
	}

	metered, err := meterAllowanceOutputs(finalTokenTx, allowance)
	if err != nil {
		return err
	}

	newSpentBytes, newStatus, appliedBytes, err := applyAllowanceCeilings(allowance, spent, metered)
	if err != nil {
		return err
	}
	// An EXHAUSTED grant is spendable only on budget freed by lazy release, and an active
	// replacement supersedes it regardless of the status this spend lands on.
	if allowance.Status == st.TokenAllowanceStatusExhausted {
		hasConflict, err := hasConflictingActiveAllowance(ctx, allowance)
		if err != nil {
			return err
		}
		if hasConflict {
			return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"%s: allowance %s cannot be spent while another active allowance exists",
				tokens.ErrAllowanceBudgetExhausted, allowanceID))
		}
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	if _, err := db.TokenAllowance.UpdateOneID(allowance.ID).
		SetSpentAmount(newSpentBytes).
		SetStatus(newStatus).
		Save(ctx); err != nil {
		// The partial-unique index (one ACTIVE grant per owner/spender/token) rejects a
		// reactivation that raced another; report it as the conflict it is.
		if ent.IsConstraintError(err) {
			return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"allowance %s cannot reactivate while another active allowance exists: %w", allowanceID, err))
		}
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update allowance %s spent amount: %w", allowanceID, err))
	}
	if _, err := db.TokenAllowanceSpend.Create().
		SetStatus(st.TokenAllowanceSpendStatusReserved).
		SetMeteredAmount(appliedBytes).
		SetTokenAllowanceID(allowance.ID).
		SetTokenTransaction(tokenTxEnt).
		Save(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to record allowance spend for allowance %s: %w", allowanceID, err))
	}
	return nil
}

func hasConflictingActiveAllowance(ctx context.Context, allowance *ent.TokenAllowance) (bool, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return false, err
	}
	exists, err := db.TokenAllowance.Query().
		Where(
			tokenallowance.IDNEQ(allowance.ID),
			tokenallowance.OwnerPublicKey(allowance.OwnerPublicKey),
			tokenallowance.SpenderPublicKey(allowance.SpenderPublicKey),
			tokenallowance.TokenCreateID(allowance.TokenCreateID),
			tokenallowance.StatusEQ(st.TokenAllowanceStatusActive),
		).
		Exist(ctx)
	if err != nil {
		return false, sparkerrors.InternalDatabaseReadError(fmt.Errorf(
			"failed to check for an active allowance conflicting with %s: %w", allowance.AllowanceID, err))
	}
	return exists, nil
}

// applyAllowanceCeilings enforces the per-transaction and lifetime ceilings on
// one metered delegated spend and returns the advanced meter, the resulting
// allowance status, and the amount the meter actually advanced by — both
// amounts encoded as the 16-byte big-endian value the uint128 columns store.
// The applied amount is what the spend row records, so releasing that
// reservation restores the meter to exactly its pre-spend value. It is a pure
// function of its inputs so the security-critical arithmetic is directly
// fuzzable (FuzzApplyAllowanceCeilings).
//
// An owner-signed unlimited flag waives its ceiling check but never the
// metering: spent_amount still advances for observability. On the unlimited
// path the meter saturates at the uint128 column ceiling (unreachable for any
// realistic token volume), bounding the applied amount at the remaining
// headroom. Inputs that cannot fit the 16-byte column at all (spent or metered
// above 2^128-1, or negative) are state corruption — upstream input/output
// balance validation bounds metered by the token supply — and fail closed
// instead of panicking in FillBytes.
func applyAllowanceCeilings(allowance *ent.TokenAllowance, spent, metered *big.Int) ([]byte, st.TokenAllowanceStatus, []byte, error) {
	if spent.Sign() < 0 || metered.Sign() < 0 || spent.Cmp(maxUint128) > 0 || metered.Cmp(maxUint128) > 0 {
		return nil, "", nil, sparkerrors.InternalDataInconsistency(fmt.Errorf(
			"allowance meter out of uint128 range: spent %s, metered %s", spent, metered))
	}
	if !allowance.PerTransactionUnlimited {
		perTxCap := new(big.Int).SetBytes(allowance.PerTransactionCap)
		if metered.Cmp(perTxCap) > 0 {
			return nil, "", nil, sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"%s: metered %s, cap %s", tokens.ErrAllowanceExceedsPerTxCap, metered, perTxCap))
		}
	}
	newSpent := new(big.Int).Add(spent, metered)
	newStatus := st.TokenAllowanceStatusActive
	if allowance.TotalUnlimited {
		// The uint128 meter column cannot hold more; saturate rather than fail a
		// spend the owner explicitly exempted from the ceiling.
		if newSpent.Cmp(maxUint128) > 0 {
			newSpent.Set(maxUint128)
		}
	} else {
		totalLimit := new(big.Int).SetBytes(allowance.TotalLimit)
		if newSpent.Cmp(totalLimit) > 0 {
			return nil, "", nil, sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"%s: spent %s + metered %s exceeds total limit %s", tokens.ErrAllowanceBudgetExhausted, spent, metered, totalLimit))
		}
		if newSpent.Cmp(totalLimit) == 0 {
			newStatus = st.TokenAllowanceStatusExhausted
		}
	}
	return newSpent.FillBytes(make([]byte, spentAmountByteLen)),
		newStatus,
		new(big.Int).Sub(newSpent, spent).FillBytes(make([]byte, spentAmountByteLen)),
		nil
}

// meterAllowanceOutputs computes the value metered against the allowance for this transaction.
// Change back to the owner is free (which is what stops laundering the limit through change);
// every other output is settled value metered against the budget, and - when the recipient
// allowlist is non-empty - must go to an allowlisted recipient. Fees are not part of the grant
// (see TODO(spark-pull) in spark_token.proto), so metered value is exactly the settled value.
func meterAllowanceOutputs(finalTokenTx *tokenpb.TokenTransaction, allowance *ent.TokenAllowance) (*big.Int, error) {
	settled := big.NewInt(0)
	for i, output := range finalTokenTx.GetTokenOutputs() {
		ownerKey, err := keys.ParsePublicKey(output.GetOwnerPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid owner public key on output %d: %w", i, err))
		}
		if ownerKey.Equals(allowance.OwnerPublicKey) {
			// Change back to the owner is not metered.
			continue
		}
		if len(allowance.RecipientAllowlist) > 0 && !allowlistContainsKey(allowance.RecipientAllowlist, ownerKey) {
			return nil, sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
				"%s: output %d", tokens.ErrAllowanceRecipientNotAllowed, i))
		}
		settled.Add(settled, new(big.Int).SetBytes(output.GetTokenAmount()))
	}
	return settled, nil
}

func allowlistContainsKey(allowlist [][]byte, key keys.Public) bool {
	serialized := key.Serialize()
	return slices.ContainsFunc(allowlist, func(entry []byte) bool {
		return bytes.Equal(entry, serialized)
	})
}

// reservedAllowanceSpendIsReleasable reports whether the transaction holding a RESERVED spend can
// no longer finalize, so its budget can safely be returned to the allowance.
func reservedAllowanceSpendIsReleasable(tx *ent.TokenTransaction) bool {
	switch tx.Status {
	case st.TokenTransactionStatusStartedCancelled, st.TokenTransactionStatusSignedCancelled:
		return true
	case st.TokenTransactionStatusStarted:
		return tx.ValidateNotExpired() != nil
	default:
		return false
	}
}

// releaseReservedSpendRows flips the given RESERVED spend rows to RELEASED and subtracts their
// metered amounts from spent in place. The caller must hold the allowance row lock; the spend
// rows themselves must already be locked (GetReservedSpendsForAllowanceForUpdate or an explicit
// ForUpdate re-read).
func releaseReservedSpendRows(ctx context.Context, spends []*ent.TokenAllowanceSpend, spent *big.Int) error {
	if len(spends) == 0 {
		return nil
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	for _, spend := range spends {
		if _, err := db.TokenAllowanceSpend.UpdateOneID(spend.ID).
			SetStatus(st.TokenAllowanceSpendStatusReleased).
			Save(ctx); err != nil {
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to release allowance spend %s: %w", spend.ID, err))
		}
		spent.Sub(spent, new(big.Int).SetBytes(spend.MeteredAmount))
	}
	if spent.Sign() < 0 {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf(
			"allowance spent amount went negative after releasing reserved spends"))
	}
	return nil
}

// getAllowanceSpendForTransaction returns the allowance spend metering the given transaction
// (any status) with its allowance eager-loaded, or nil when the transaction is owner-signed.
//
// When forUpdate is set the spend row is locked FOR UPDATE. This is the serialization point
// between finalizing a delegated transaction and lazy release: the lazy-release path
// (GetReservedSpendsForAllowanceForUpdate) locks the same rows, so a signing gate that holds
// this lock from its check until commit can never race a release of the same spend.
//
// LOCK ORDER: callers that lock must already hold the transaction-data locks
// (FetchAndLockTokenTransactionData*), never the reverse. Under lock this takes the spend row
// and then its allowance row; revocation locks only the allowance, so the orders cannot cycle.
func getAllowanceSpendForTransaction(ctx context.Context, tokenTransactionID uuid.UUID, forUpdate bool) (*ent.TokenAllowanceSpend, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	readSpend := func(lock bool) (*ent.TokenAllowanceSpend, error) {
		query := db.TokenAllowanceSpend.Query().
			Where(tokenallowancespend.HasTokenTransactionWith(tokentransaction.IDEQ(tokenTransactionID)))
		if lock {
			query = query.ForUpdate()
		}
		spend, err := query.WithTokenAllowance().Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, nil
			}
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf(
				"failed to query allowance spend for transaction %s: %w", tokenTransactionID, err))
		}
		if spend.Edges.TokenAllowance == nil {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("allowance spend %s has no allowance edge", spend.ID))
		}
		if lock {
			// The eager-loaded allowance is not covered by the spend row lock, and revocation
			// locks the allowance row rather than the spend. Re-read it FOR UPDATE so the
			// status this checkpoint acts on cannot change between here and commit.
			lockedAllowance, err := ent.GetAllowanceByAllowanceIDForUpdate(ctx, spend.Edges.TokenAllowance.AllowanceID)
			if err != nil {
				return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf(
					"failed to lock allowance %s for spend %s: %w", spend.Edges.TokenAllowance.AllowanceID, spend.ID, err))
			}
			spend.Edges.TokenAllowance = lockedAllowance
		}
		return spend, nil
	}

	spend, err := readSpend(false)
	if err != nil || spend == nil || !forUpdate {
		return spend, err
	}

	// An eager-loaded allowance is an unlocked read, so a revocation committing before this
	// transaction commits would go unseen. The unlocked read above supplies only the allowance
	// id, keeping the allowance -> spend lock order the metering path relies on.
	lockedAllowance, err := ent.GetAllowanceByAllowanceIDForUpdate(ctx, spend.Edges.TokenAllowance.AllowanceID)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf(
			"failed to lock allowance %s for transaction %s: %w", spend.Edges.TokenAllowance.AllowanceID, tokenTransactionID, err))
	}
	lockedSpend, err := readSpend(true)
	if err != nil || lockedSpend == nil {
		return lockedSpend, err
	}
	lockedSpend.Edges.TokenAllowance = lockedAllowance
	return lockedSpend, nil
}

// errAllowanceSpendReleased reports a returned reservation, which callers must distinguish from
// an owner revoking the grant.
var errAllowanceSpendReleased = errors.New("allowance budget for this transaction was released; the transaction can no longer proceed")

// checkAllowanceSpendCanProceed enforces the shared allowance checkpoint invariants: if the
// transaction was authorized under an allowance, it can only proceed while its metered spend is
// still RESERVED and the allowance is still spendable. A RELEASED spend means the budget was
// already returned (the transaction lost a preemption race or expired), so letting it proceed
// would complete an unmetered delegated spend. No re-metering happens here. A nil spend means the
// transaction is owner-signed and no allowance policy applies.
func checkAllowanceSpendCanProceed(spend *ent.TokenAllowanceSpend) error {
	if spend == nil {
		return nil
	}
	if spend.Status != st.TokenAllowanceSpendStatusReserved {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"%w (spend status %s)", errAllowanceSpendReleased, spend.Status))
	}
	return checkAllowanceSpendable(spend.Edges.TokenAllowance)
}

// checkAllowanceSpendable reports whether a grant may still authorize settlement, enforcing both
// limits the owner signed: revocation and the expiry window. A status this build does not
// recognize is rejected, so a value written by a newer operator is never assumed permissive.
func checkAllowanceSpendable(allowance *ent.TokenAllowance) error {
	switch allowance.Status {
	case st.TokenAllowanceStatusActive, st.TokenAllowanceStatusExhausted:
	case st.TokenAllowanceStatusRevoked:
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"%s: %s", tokens.ErrAllowanceRevoked, allowance.AllowanceID))
	default:
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"%s: allowance %s has unrecognized status %q", tokens.ErrAllowanceNotSpendable, allowance.AllowanceID, allowance.Status))
	}
	if !allowance.ExpiryTime.After(time.Now()) {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"%s: %s (expired at %s)", tokens.ErrAllowanceExpired, allowance.AllowanceID, allowance.ExpiryTime.UTC().Format(time.RFC3339)))
	}
	return nil
}

// allowanceExchangeBlockedMessage names why a pre-reveal checkpoint rejected a delegated
// transaction. A returned reservation and an owner revocation are separate operational events.
func allowanceExchangeBlockedMessage(err error) string {
	if errors.Is(err, errAllowanceSpendReleased) {
		return "released allowance budget cannot exchange revocation secrets"
	}
	return "revoked allowance cannot exchange revocation secrets"
}

// validateAllowanceNotRevokedForTransaction is the fail-fast allowance checkpoint, mirroring the
// freeze checks. It takes no lock, so it must not be the last line of defense before a
// transaction commits a status transition: use validateAllowanceSpendReservedForSigning for
// that. It exists for entry points that reject early before any transaction-data locks are held.
func validateAllowanceNotRevokedForTransaction(ctx context.Context, tokenTransactionEnt *ent.TokenTransaction) error {
	spend, err := getAllowanceSpendForTransaction(ctx, tokenTransactionEnt.ID, false)
	if err != nil {
		return err
	}
	return checkAllowanceSpendCanProceed(spend)
}

// validateAllowanceSpendReservedForSigning is the authoritative sign-time allowance checkpoint.
// It locks the transaction's spend row FOR UPDATE and holds the lock until the surrounding
// database transaction commits, which serializes the check-then-sign/finalize sequence against
// lazy release: budget can never be returned for a spend whose transaction passed this gate but
// has not yet committed. Callers must already hold the transaction-data locks
// (FetchAndLockTokenTransactionData*) - see getAllowanceSpendForTransaction for the lock order.
func validateAllowanceSpendReservedForSigning(ctx context.Context, tokenTransactionEnt *ent.TokenTransaction) error {
	spend, err := getAllowanceSpendForTransaction(ctx, tokenTransactionEnt.ID, true)
	if err != nil {
		return err
	}
	return checkAllowanceSpendCanProceed(spend)
}

// validateAllowanceNotRevokedForTransactionHash is the by-hash variant used by the coordinator's
// pre-reveal checkpoint, where only the finalized transaction hash is at hand. Like the by-ent
// variant it is fail-fast only; the transaction was already gated under lock when it was signed.
func validateAllowanceNotRevokedForTransactionHash(ctx context.Context, tokenTransactionHash []byte) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	tokenTransactionID, err := db.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(tokenTransactionHash)).
		OnlyID(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return sparkerrors.NotFoundMissingEntity(fmt.Errorf(
				"token transaction not found for allowance revocation check (txHash: %x)", tokenTransactionHash))
		}
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf(
			"failed to fetch token transaction for allowance revocation check (txHash: %x): %w", tokenTransactionHash, err))
	}
	spend, err := getAllowanceSpendForTransaction(ctx, tokenTransactionID, false)
	if err != nil {
		return err
	}
	return checkAllowanceSpendCanProceed(spend)
}

// loadAllowanceForSigningIfDelegated resolves the allowance to validate operator-specific input
// signatures against at sign time. When the signatures cite an allowance, the transaction must
// carry a RESERVED spend for that same allowance (recorded at prepare), and the allowance must be
// neither revoked nor expired. Returns nil for owner-signed transactions. The spend row is
// locked FOR UPDATE (callers run under the transaction-data locks) so the reservation cannot be
// lazily released between this check and the signing transaction's commit.
func loadAllowanceForSigningIfDelegated(
	ctx context.Context,
	sigs []*tokenpb.SignatureWithIndex,
	tokenTransactionEnt *ent.TokenTransaction,
) (*ent.TokenAllowance, error) {
	allowanceID, err := extractAllowanceAuthorization(sigs)
	if err != nil {
		return nil, err
	}
	if allowanceID == nil {
		return nil, nil
	}
	spend, err := getAllowanceSpendForTransaction(ctx, tokenTransactionEnt.ID, true)
	if err != nil {
		return nil, err
	}
	if spend == nil {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"input signatures cite allowance %s but the transaction has no metered allowance spend", *allowanceID))
	}
	allowance := spend.Edges.TokenAllowance
	if allowance.AllowanceID != *allowanceID {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"input signatures cite allowance %s but the transaction was prepared under allowance %s", *allowanceID, allowance.AllowanceID))
	}
	if err := checkAllowanceSpendCanProceed(spend); err != nil {
		return nil, err
	}
	return allowance, nil
}

// releaseAllowanceSpendsForTransactions returns reserved allowance budget held by transactions
// that lost a preemption race. Idempotent: only RESERVED rows are flipped and a re-run over
// already-released transactions is a no-op. Allowances are locked in ascending allowance_id
// order so concurrent releases take row locks deterministically.
func releaseAllowanceSpendsForTransactions(ctx context.Context, losingTxs []*ent.TokenTransaction) error {
	if len(losingTxs) == 0 {
		return nil
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	txIDs := make([]uuid.UUID, 0, len(losingTxs))
	for _, tx := range losingTxs {
		txIDs = append(txIDs, tx.ID)
	}

	// Unlocked candidate read; the authoritative re-read happens under the allowance lock below.
	candidates, err := db.TokenAllowanceSpend.Query().
		Where(
			tokenallowancespend.StatusEQ(st.TokenAllowanceSpendStatusReserved),
			tokenallowancespend.HasTokenTransactionWith(tokentransaction.IDIn(txIDs...)),
		).
		WithTokenAllowance().
		All(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query allowance spends for preempted transactions: %w", err))
	}
	if len(candidates) == 0 {
		return nil
	}

	spendIDsByAllowanceID := make(map[uuid.UUID][]uuid.UUID)
	for _, spend := range candidates {
		allowance := spend.Edges.TokenAllowance
		if allowance == nil {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf("allowance spend %s has no allowance edge", spend.ID))
		}
		spendIDsByAllowanceID[allowance.AllowanceID] = append(spendIDsByAllowanceID[allowance.AllowanceID], spend.ID)
	}
	sortedAllowanceIDs := make([]uuid.UUID, 0, len(spendIDsByAllowanceID))
	for allowanceID := range spendIDsByAllowanceID {
		sortedAllowanceIDs = append(sortedAllowanceIDs, allowanceID)
	}
	slices.SortFunc(sortedAllowanceIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	for _, allowanceID := range sortedAllowanceIDs {
		allowance, err := ent.GetAllowanceByAllowanceIDForUpdate(ctx, allowanceID)
		if err != nil {
			return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to lock allowance %s for release: %w", allowanceID, err))
		}
		// Re-read under the allowance lock so a concurrent release cannot double-refund.
		lockedSpends, err := db.TokenAllowanceSpend.Query().
			Where(
				tokenallowancespend.IDIn(spendIDsByAllowanceID[allowanceID]...),
				tokenallowancespend.StatusEQ(st.TokenAllowanceSpendStatusReserved),
			).
			ForUpdate().
			All(ctx)
		if err != nil {
			return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to lock allowance spends for release: %w", err))
		}
		if len(lockedSpends) == 0 {
			continue
		}
		spent := new(big.Int).SetBytes(allowance.SpentAmount)
		if err := releaseReservedSpendRows(ctx, lockedSpends, spent); err != nil {
			return err
		}
		update := db.TokenAllowance.UpdateOneID(allowance.ID).
			SetSpentAmount(spent.FillBytes(make([]byte, spentAmountByteLen)))
		// Freed budget reactivates an EXHAUSTED allowance; REVOKED is a permanent tombstone.
		if allowance.Status == st.TokenAllowanceStatusExhausted &&
			spent.Cmp(new(big.Int).SetBytes(allowance.TotalLimit)) < 0 {
			hasConflict, err := hasConflictingActiveAllowance(ctx, allowance)
			if err != nil {
				return err
			}
			if !hasConflict {
				update = update.SetStatus(st.TokenAllowanceStatusActive)
			}
		}
		if _, err := update.Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf(
					"allowance %s cannot reactivate while another active allowance exists: %w", allowanceID, err))
			}
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to restore allowance %s budget: %w", allowanceID, err))
		}
	}
	return nil
}
