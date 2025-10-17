package errors

import (
	"google.golang.org/grpc/codes"
)

// Canonical reason constants for ErrorInfo.Reason. Keep stable, UPPER_SNAKE_CASE.  All errors should have a grpc error code prefix.
const (
	ReasonInternalDatabaseMissingEdge          = "MISSING_EDGE"
	ReasonInternalDatabaseTransactionLifecycle = "DATABASE_TRANSACTION_LIFECYCLE"
	ReasonInternalDatabaseWrite                = "DATABASE_WRITE"
	ReasonInternalDatabaseRead                 = "DATABASE_READ"
	ReasonInternalTypeConversion               = "TYPE_CONVERSION"
	ReasonInternalUnhandled                    = "UNHANDLED"
	ReasonInternalObjectNull                   = "INTERNAL_OBJECT_NULL"
	ReasonInternalObjectMissingField           = "INTERNAL_OBJECT_MISSING_FIELD"
	ReasonInternalObjectMalformedField         = "INTERNAL_OBJECT_MALFORMED_FIELD"
	ReasonInternalObjectOutOfRange             = "INTERNAL_OBJECT_OUT_OF_RANGE"
	ReasonInternalKeyshareError                = "INTERNAL_KEYSHARE_ERROR"
	ReasonInternalInvalidOperatorResponse      = "INVALID_OPERATOR_RESPONSE"

	ReasonInvalidArgumentMissingField      = "MISSING_FIELD"
	ReasonInvalidArgumentMalformedField    = "MALFORMED_FIELD"
	ReasonInvalidArgumentDuplicateField    = "DUPLICATE_FIELD"
	ReasonInvalidArgumenMalformedKey       = "MALFORMED_KEY"
	ReasonInvalidArgumentInvalidVersion    = "INVALID_VERSION"
	ReasonInvalidArgumentPublicKeyMismatch = "PUBLIC_KEY_MISMATCH"
	ReasonInvalidArgumentOutOfRange        = "OUT_OF_RANGE"

	ReasonFailedPreconditionBadSignature              = "BAD_SIGNATURE"
	ReasonFailedPreconditionTokenRulesViolation       = "TOKEN_RULES_VIOLATION"
	ReasonFailedPreconditionInsufficientConfirmations = "INSUFFICIENT_CONFIRMATIONS"
	ReasonFailedPreconditionInvalidState              = "INVALID_STATE"
	ReasonFailedPreconditionExpired                   = "EXPIRED"
	ReasonFailedPreconditionReplay                    = "REPLAY"
	ReasonFailedPreconditionHashMismatch              = "HASH_MISMATCH"

	ReasonAbortedTransactionPreempted = "TRANSACTION_PREEMPTED"

	ReasonAlreadyExistsDuplicateOperation = "DUPLICATE_OPERATION"

	ReasonNotFoundMissingEntity = "MISSING_ENTITY"

	ReasonResourceExhaustedRateLimitExceeded        = "RATE_LIMIT_EXCEEDED"
	ReasonResourceExhaustedConcurrencyLimitExceeded = "CONCURRENCY_LIMIT_EXCEEDED"

	ReasonUnavailableMethodDisabled   = "METHOD_DISABLED"
	ReasonUnavailableDataStore        = "DATA_STORE_UNAVAILABLE"
	ReasonUnavailableDatabaseTimeout  = "DATABASE_TIMEOUT"
	ReasonUnavailableExternalOperator = "EXTERNAL_OPERATOR_UNAVAILABLE"

	// ErrorReasonPrefixFailedWithExternalCoordinator is a prefix for errors that occur when the coordinator calls out to another
	// coordinator and that call fails. The underlying reason from the external coordinator should be appended after a colon.
	ErrorReasonPrefixFailedWithExternalCoordinator = "FAILED_WITH_EXTERNAL_COORDINATOR"
)

func InternalTypeConversionError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalTypeConversion)
}

func InternalUnhandledError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalUnhandled)
}

func InternalDatabaseTransactionLifecycleError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalDatabaseTransactionLifecycle)
}

func InternalDatabaseWriteError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalDatabaseWrite)
}

func InternalDatabaseReadError(err error) error {
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

func InternalObjectOutOfRange(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalObjectOutOfRange)
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

func InvalidArgumentOutOfRange(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentOutOfRange)
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

func FailedPreconditionExpired(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionExpired)
}

func FailedPreconditionReplay(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionReplay)
}

func FailedPreconditionHashMismatch(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionHashMismatch)
}

func AbortedTransactionPreempted(err error) error {
	return newGRPCError(codes.Aborted, err, ReasonAbortedTransactionPreempted)
}

func AlreadyExistsDuplicateOperation(err error) error {
	return newGRPCError(codes.AlreadyExists, err, ReasonAlreadyExistsDuplicateOperation)
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

func UnimplementedMethodDisabled(err error) error {
	return newGRPCError(codes.Unimplemented, err, ReasonUnavailableMethodDisabled)
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
