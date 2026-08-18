package errors

import (
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Default retry hint surfaced as google.rpc.RetryInfo for Aborted errors
// caused by short-lived row-lock contention (typically resolved in <1s).
const abortedLockConflictRetryAfter = 100 * time.Millisecond

// ErrShuttingDown is returned by streaming RPC handlers that proactively
// terminate when the server starts shutting down. The log interceptor
// recognizes this error and skips error logging because the situation is
// expected.
var ErrShuttingDown = status.Error(codes.Unavailable, "server shutting down")

// Canonical reason constants for ErrorInfo.Reason. Keep stable, UPPER_SNAKE_CASE.  All errors should have a grpc error code prefix.
const (
	ReasonInternalDatabaseMissingEdge          = "MISSING_EDGE"
	ReasonInternalDatabaseTransactionLifecycle = "DATABASE_TRANSACTION_LIFECYCLE"
	ReasonInternalDatabaseWrite                = "DATABASE_WRITE"
	ReasonInternalDatabaseRead                 = "DATABASE_READ"
	ReasonInternalTypeConversion               = "TYPE_CONVERSION"
	ReasonInternalUnhandled                    = "UNHANDLED"
	ReasonInternalDataInconsistency            = "DATA_INCONSISTENCY"
	ReasonInternalObjectNull                   = "INTERNAL_OBJECT_NULL"
	ReasonInternalObjectMissingField           = "INTERNAL_OBJECT_MISSING_FIELD"
	ReasonInternalObjectMalformedField         = "INTERNAL_OBJECT_MALFORMED_FIELD"
	ReasonInternalObjectOutOfRange             = "INTERNAL_OBJECT_OUT_OF_RANGE"
	ReasonInternalKeyshareError                = "INTERNAL_KEYSHARE_ERROR"
	ReasonInternalInvalidOperatorResponse      = "INVALID_OPERATOR_RESPONSE"
	ReasonInternalOperationTooSlow             = "OPERATION_TOO_SLOW"
	ReasonInternalSigningFailure               = "SIGNING_FAILURE"

	ReasonInvalidArgumentMissingField        = "MISSING_FIELD"
	ReasonInvalidArgumentMalformedField      = "MALFORMED_FIELD"
	ReasonInvalidArgumentDuplicateField      = "DUPLICATE_FIELD"
	ReasonInvalidArgumenMalformedKey         = "MALFORMED_KEY"
	ReasonInvalidArgumentInvalidVersion      = "INVALID_VERSION"
	ReasonInvalidArgumentPublicKeyMismatch   = "PUBLIC_KEY_MISMATCH"
	ReasonInvalidArgumentHashMismatch        = "HASH_MISMATCH"
	ReasonInvalidArgumentOutOfRange          = "OUT_OF_RANGE"
	ReasonInvalidArgumentNetworkNotSupported = "NETWORK_NOT_SUPPORTED"
	ReasonInvalidArgumentLeafRenewalRequired = "LEAF_RENEWAL_REQUIRED"
	ReasonInvalidArgumentTimelockMismatch    = "TIMELOCK_MISMATCH"

	ReasonInvalidArgumentMpcAuthorizationSignatureInvalid = "MPC_AUTHORIZATION_SIGNATURE_INVALID"
	ReasonInvalidArgumentMpcSubShareUnsealable            = "MPC_SUBSHARE_UNSEALABLE"
	ReasonInvalidArgumentMpcSubShareInvalid               = "MPC_SUBSHARE_INVALID"
	ReasonInvalidArgumentMpcTweakBindingMismatch          = "MPC_TWEAK_BINDING_MISMATCH"

	ReasonFailedPreconditionBadSignature              = "BAD_SIGNATURE"
	ReasonFailedPreconditionTokenRulesViolation       = "TOKEN_RULES_VIOLATION"
	ReasonFailedPreconditionInsufficientConfirmations = "INSUFFICIENT_CONFIRMATIONS"
	ReasonFailedPreconditionInvalidState              = "INVALID_STATE"
	ReasonFailedPreconditionLeafUnavailable           = "LEAF_UNAVAILABLE"
	ReasonFailedPreconditionExpired                   = "EXPIRED"
	ReasonFailedPreconditionReplay                    = "REPLAY"
	ReasonFailedPreconditionHashMismatch              = "HASH_MISMATCH"
	ReasonFailedPreconditionMpcAuthorizationMismatch  = "MPC_AUTHORIZATION_MISMATCH"

	ReasonAbortedTransactionPreempted       = "TRANSACTION_PREEMPTED"
	ReasonAbortedConcurrentClaimConflict    = "CONCURRENT_CLAIM_CONFLICT"
	ReasonAbortedConcurrentKeyshareRotation = "CONCURRENT_KEYSHARE_ROTATION"
	ReasonAbortedLockConflict               = "LOCK_CONFLICT"

	ReasonAlreadyExistsDuplicateOperation = "DUPLICATE_OPERATION"
	ReasonAlreadyExistsExpiredTransaction = "EXPIRED_TRANSACTION"

	ReasonNotFoundMissingEntity = "MISSING_ENTITY"

	ReasonResourceExhaustedRateLimitExceeded        = "RATE_LIMIT_EXCEEDED"
	ReasonResourceExhaustedConcurrencyLimitExceeded = "CONCURRENCY_LIMIT_EXCEEDED"
	ReasonResourceExhaustedQuotaExceeded            = "QUOTA_EXCEEDED"

	ReasonPermissionDeniedNoReadAccess = "NO_READ_ACCESS"

	ReasonUnavailableMethodDisabled   = "METHOD_DISABLED"
	ReasonUnavailableDataStore        = "DATA_STORE_UNAVAILABLE"
	ReasonUnavailableDatabaseTimeout  = "DATABASE_TIMEOUT"
	ReasonUnavailableExternalOperator = "EXTERNAL_OPERATOR_UNAVAILABLE"

	// ReasonUnimplementedFeatureIncomplete marks a request that passed every implemented check on a method whose
	// remaining stages have not shipped yet — distinguishing "accepted as far as the code goes" from METHOD_DISABLED
	// ("this method is switched off"), which shares the Unimplemented code.
	ReasonUnimplementedFeatureIncomplete = "FEATURE_NOT_IMPLEMENTED"

	// ErrorReasonPrefixFailedWithExternalCoordinator is a prefix for errors that occur when the coordinator calls out to another
	// coordinator and that call fails. The underlying reason from the external coordinator should be appended after a colon.
	ErrorReasonPrefixFailedWithExternalCoordinator = "FAILED_WITH_EXTERNAL_COORDINATOR"
)

// Keys for ErrorInfo.Metadata entries. Keep stable: callers parse these to
// identify the offending entity without matching on message text.
const (
	ErrorMetadataLeafID           = "leaf_id"
	ErrorMetadataExpectedTimelock = "expected_timelock"
	ErrorMetadataProvidedTimelock = "provided_timelock"
)

func InternalTypeConversionError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalTypeConversion)
}

func InternalUnhandledError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalUnhandled)
}

func InternalDataInconsistency(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalDataInconsistency)
}

func InternalDatabaseTransactionLifecycleError(err error) error {
	if IsTransientDBContention(err) {
		return AbortedLockConflict(err)
	}
	return newGRPCError(codes.Internal, err, ReasonInternalDatabaseTransactionLifecycle)
}

func InternalDatabaseWriteError(err error) error {
	if IsTransientDBContention(err) {
		return AbortedLockConflict(err)
	}
	return newGRPCError(codes.Internal, err, ReasonInternalDatabaseWrite)
}

func InternalDatabaseReadError(err error) error {
	if IsTransientDBContention(err) {
		return AbortedLockConflict(err)
	}
	return newGRPCError(codes.Internal, err, ReasonInternalDatabaseRead)
}

func InternalDatabaseMissingEdge(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalDatabaseMissingEdge)
}

// Use for internal objects not provided by the caller.
func InternalObjectNull(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalObjectNull)
}

// Use for internal objects not provided by the caller.
func InternalObjectMissingField(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalObjectMissingField)
}

// Use for internal objects not provided by the caller.
func InternalObjectMalformedField(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalObjectMalformedField)
}

func InternalInvalidOperatorResponse(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalInvalidOperatorResponse)
}

func InternalKeyshareError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalKeyshareError)
}

// Use for failures in the FROST signing/aggregation pipeline (signer RPCs,
// share bookkeeping) that are not attributable to caller input.
func InternalSigningError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalSigningFailure)
}

func InternalObjectOutOfRange(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalObjectOutOfRange)
}

func InternalOperationTooSlow(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalOperationTooSlow)
}

// Use for external objects provided by the caller
func InvalidArgumentMissingField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMissingField)
}

// Use for external objects provided by the caller
func InvalidArgumentMalformedField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMalformedField)
}

// Use for external objects provided by the caller
func InvalidArgumentDuplicateField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentDuplicateField)
}

// Use for external objects provided by the caller
func InvalidArgumentMalformedKey(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumenMalformedKey)
}

// Use for external objects provided by the caller
func InvalidArgumentInvalidVersion(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentInvalidVersion)
}

// Use for external objects provided by the caller
func InvalidArgumentPublicKeyMismatch(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentPublicKeyMismatch)
}

// Use when a hash inside the request disagrees with another part of the same
// request (e.g. an invoice's payment hash vs the request's payment_hash field,
// or a claimed transaction hash vs the hash recomputed from the request body).
// Such a request can never succeed, no matter the system state. For a hash
// checked against server-side state, use FailedPreconditionHashMismatch.
func InvalidArgumentHashMismatch(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentHashMismatch)
}

func InvalidArgumentOutOfRange(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentOutOfRange)
}

func InvalidArgumentNetworkNotSupported(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentNetworkNotSupported)
}

// Use when a leaf's refund timelock is at the floor and the leaf cannot be
// transferred until renewed. Callers with the leaf in scope should attach its
// id via WrapErrorWithMetadata(err, {ErrorMetadataLeafID: ...}).
func InvalidArgumentLeafRenewalRequired(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentLeafRenewalRequired)
}

// Use when the caller's proposed refund timelock disagrees with the operator's
// expected next timelock for the leaf. Callers with the leaf in scope should
// attach its id via WrapErrorWithMetadata(err, {ErrorMetadataLeafID: ...}).
func InvalidArgumentTimelockMismatch(err error, expectedTimelock, providedTimelock uint32) error {
	e := newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentTimelockMismatch)
	e.Metadata = map[string]string{
		ErrorMetadataExpectedTimelock: strconv.FormatUint(uint64(expectedTimelock), 10),
		ErrorMetadataProvidedTimelock: strconv.FormatUint(uint64(providedTimelock), 10),
	}
	return e
}

func FailedPreconditionBadSignature(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionBadSignature)
}

func FailedPreconditionTokenRulesViolation(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionTokenRulesViolation)
}

func FailedPreconditionInsufficientConfirmations(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionInsufficientConfirmations)
}

func FailedPreconditionInvalidState(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionInvalidState)
}

func FailedPreconditionLeafUnavailable(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionLeafUnavailable)
}

// Use when a multiparty transfer authorization's signature does not verify over the recomputed submission payload —
// the submission can never succeed as signed, distinguishing it from a fact that merely disagrees with current state.
func InvalidArgumentMpcAuthorizationSignatureInvalid(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMpcAuthorizationSignatureInvalid)
}

// Use when a validly-signed multiparty transfer authorization names a fact that disagrees with this operator's own
// state (leaf ownership, value, owner signing key, receiver-bound outputs, refund sighashes, expiry).
func FailedPreconditionMpcAuthorizationMismatch(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionMpcAuthorizationMismatch)
}

// Use when a sealed multiparty sub-share cannot be decrypted, or its decrypted payload misdescribes the transfer;
// the failing participant position is named in the error.
func InvalidArgumentMpcSubShareUnsealable(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMpcSubShareUnsealable)
}

// Use when an unsealed multiparty sub-share fails validation against its signed commitment vector.
func InvalidArgumentMpcSubShareInvalid(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMpcSubShareInvalid)
}

// Use when the combined multiparty tweak commitment does not equal (leaf owner pubkey − signed mask commitment) —
// a wrong or inconsistent mask, rejected before any state change instead of bricking the leaf after commit.
func InvalidArgumentMpcTweakBindingMismatch(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMpcTweakBindingMismatch)
}

func FailedPreconditionExpired(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionExpired)
}

func FailedPreconditionReplay(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionReplay)
}

// Use when a hash from the request disagrees with server-side state (e.g. a
// provided preimage vs the payment hash stored for the swap). For a hash that
// is inconsistent within the request itself, use InvalidArgumentHashMismatch.
func FailedPreconditionHashMismatch(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionHashMismatch)
}

// AbortedTransactionPreempted intentionally omits a RetryInfo hint. The
// production caller (tokens.NewTransactionPreemptedError) fires for inputs
// that are "in progress or finalized" — finalized inputs are permanently
// spent and a retry will only produce the same error. Clients that know
// their conflict is transient can still retry on the bare Aborted code per
// gRPC convention; we just don't promise that retry will succeed.
func AbortedTransactionPreempted(err error) error {
	return newGRPCError(codes.Aborted, err, ReasonAbortedTransactionPreempted)
}

func AbortedConcurrentClaimConflict(err error) error {
	return newRetryableGRPCError(codes.Aborted, err, ReasonAbortedConcurrentClaimConflict, abortedLockConflictRetryAfter)
}

func AbortedConcurrentKeyshareRotation(err error) error {
	return newRetryableGRPCError(codes.Aborted, err, ReasonAbortedConcurrentKeyshareRotation, abortedLockConflictRetryAfter)
}

func AbortedLockConflict(err error) error {
	return newRetryableGRPCError(codes.Aborted, err, ReasonAbortedLockConflict, abortedLockConflictRetryAfter)
}

func AlreadyExistsDuplicateOperation(err error) error {
	return newGRPCError(codes.AlreadyExists, err, ReasonAlreadyExistsDuplicateOperation)
}

func AlreadyExistsExpiredTransaction(err error) error {
	return newGRPCError(codes.AlreadyExists, err, ReasonAlreadyExistsExpiredTransaction)
}

func NotFoundMissingEntity(err error) error {
	return newGRPCError(codes.NotFound, err, ReasonNotFoundMissingEntity)
}

func ResourceExhaustedRateLimitExceeded(err error) error {
	return newGRPCError(codes.ResourceExhausted, err, ReasonResourceExhaustedRateLimitExceeded)
}

func ResourceExhaustedConcurrencyLimitExceeded(err error) error {
	return newGRPCError(codes.ResourceExhausted, err, ReasonResourceExhaustedConcurrencyLimitExceeded)
}

// ResourceExhaustedQuotaExceeded is for when a per-owner or per-object stored-resource
// quota is hit. Not retryable until the caller frees quota.
func ResourceExhaustedQuotaExceeded(err error) error {
	return newGRPCError(codes.ResourceExhausted, err, ReasonResourceExhaustedQuotaExceeded)
}

func PermissionDeniedNoReadAccess(err error) error {
	return newGRPCError(codes.PermissionDenied, err, ReasonPermissionDeniedNoReadAccess)
}

func UnimplementedMethodDisabled(err error) error {
	return newGRPCError(codes.Unimplemented, err, ReasonUnavailableMethodDisabled)
}

func UnimplementedFeatureIncomplete(err error) error {
	return newGRPCError(codes.Unimplemented, err, ReasonUnimplementedFeatureIncomplete)
}

func UnavailableMethodDisabled(err error) error {
	return newGRPCError(codes.Unavailable, err, ReasonUnavailableMethodDisabled)
}

func UnavailableDatabaseTimeout(err error) error {
	return newGRPCError(codes.Unavailable, err, ReasonUnavailableDatabaseTimeout)
}

func UnavailableDataStore(err error) error {
	return newGRPCError(codes.Unavailable, err, ReasonUnavailableDataStore)
}

func UnavailableExternalOperator(err error) error {
	return newGRPCError(codes.Unavailable, err, ReasonUnavailableExternalOperator)
}
