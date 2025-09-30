package errors

import (
	"google.golang.org/grpc/codes"
)

// Canonical reason constants for ErrorInfo.Reason. Keep stable, UPPER_SNAKE_CASE.  All errors should have a grpc error code prefix.
const (
	ReasonInternalDatabaseError       = "DATABASE_ERROR"
	ReasonInternalTypeConversionError = "TYPE_CONVERSION_ERROR"
	ReasonInternalUnhandledError      = "UNHANDLED_ERROR"

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
	ReasonNotFoundMissingEdge   = "MISSING_EDGE"

	ReasonResourceExhaustedRateLimitExceeded        = "RATE_LIMIT_EXCEEDED"
	ReasonResourceExhaustedConcurrencyLimitExceeded = "CONCURRENCY_LIMIT_EXCEEDED"

	ReasonUnavailableMethodDisabled  = "METHOD_DISABLED"
	ReasonUnavailableDataStore       = "DATA_STORE_UNAVAILABLE"
	ReasonUnavailableDatabaseTimeout = "DATABASE_TIMEOUT"

	// ErrorReasonPrefixFailedWithExternalCoordinator is a prefix for errors that occur when the coordinator calls out to another
	// coordinator and that call fails. The underlying reason from the external coordinator should be appended after a colon.
	ErrorReasonPrefixFailedWithExternalCoordinator = "FAILED_WITH_EXTERNAL_COORDINATOR"
)

func InternalTypeConversionError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalTypeConversionError)
}

func InternalUnhandledError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalUnhandledError)
}

func InternalDatabaseError(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalDatabaseError)
}

func InvalidArgumentMissingField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMissingField)
}

func InvalidArgumentMalformedField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMalformedField)
}

func InvalidArgumentDuplicateField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentDuplicateField)
}

func InvalidArgumentMalformedKey(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumenMalformedKey)
}

func InvalidArgumentInvalidVersion(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentInvalidVersion)
}

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

func NotFoundMissingEdge(err error) error {
	return newGRPCError(codes.NotFound, err, ReasonNotFoundMissingEdge)
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
