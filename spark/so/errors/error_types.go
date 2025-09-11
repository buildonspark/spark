package errors

import (
	"fmt"

	"google.golang.org/grpc/codes"
)

// Canonical reason constants for ErrorInfo.Reason. Keep stable, UPPER_SNAKE_CASE.  All errors should have a grpc error code prefix.
const (
	ReasonInternalDependencyFailure = "DEPENDENCY_FAILURE"

	ReasonInvalidArgumentMissingField   = "MISSING_FIELD"
	ReasonInvalidArgumentMalformedField = "MALFORMED_FIELD"
	ReasonInvalidArgumenMalformedKey    = "MALFORMED_KEY"

	ReasonFailedPreconditionNotSpendable              = "NOT_SPENDABLE"
	ReasonFailedPreconditionBadSignature              = "BAD_SIGNATURE"
	ReasonFailedPreconditionTokenRulesViolation       = "TOKEN_RULES_VIOLATION"
	ReasonFailedPreconditionInsufficientConfirmations = "INSUFFICIENT_CONFIRMATIONS"

	ReasonAbortedTransactionPreempted = "TRANSACTION_PREEMPTED"

	ReasonAlreadyExistsDuplicateOperation = "DUPLICATE_OPERATION"

	ReasonNotFoundMissingEntity = "MISSING_ENTITY"
	ReasonNotFoundMissingEdge   = "MISSING_EDGE"

	ReasonResourceExhaustedRateLimitExceeded        = "RATE_LIMIT_EXCEEDED"
	ReasonResourceExhaustedConcurrencyLimitExceeded = "CONCURRENCY_LIMIT_EXCEEDED"

	// ErrorReasonPrefixFailedWithExternalCoordinator is a prefix for errors that occur when the coordinator calls out to another
	// coordinator and that call fails. The underlying reason from the external coordinator should be appended after a colon.
	ErrorReasonPrefixFailedWithExternalCoordinator = "FAILED_WITH_EXTERNAL_COORDINATOR"
)

func InternalDependencyFailure(err error) error {
	return newGRPCError(codes.Internal, err, ReasonInternalDependencyFailure)
}

func InvalidArgumentMissingField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMissingField)
}

func InvalidArgumentMalformedField(err error) error {
	return newGRPCError(codes.InvalidArgument, err, ReasonInvalidArgumentMalformedField)
}

func FailedPreconditionNotSpendable(err error) error {
	return newGRPCError(codes.FailedPrecondition, err, ReasonFailedPreconditionNotSpendable)
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

// ------------------------------------------------------------
// IMPORTANT: These methods are deprecated in favor of migrating to error types with reason.
// ------------------------------------------------------------
func InvalidUserInputErrorf(format string, args ...any) error {
	return newGRPCError(codes.InvalidArgument, fmt.Errorf(format, args...), "")
}

func FailedPreconditionErrorf(format string, args ...any) error {
	ge := newGRPCError(codes.FailedPrecondition, fmt.Errorf(format, args...), "")
	return ge
}

func NotFoundErrorf(format string, args ...any) error {
	ge := newGRPCError(codes.NotFound, fmt.Errorf(format, args...), "")
	return ge
}

func UnavailableErrorf(format string, args ...any) error {
	ge := newGRPCError(codes.Unavailable, fmt.Errorf(format, args...), "")
	return ge
}

func AlreadyExistsErrorf(format string, args ...any) error {
	ge := newGRPCError(codes.AlreadyExists, fmt.Errorf(format, args...), "")
	return ge
}
func UnimplementedErrorf(format string, args ...any) error {
	ge := newGRPCError(codes.Unimplemented, fmt.Errorf(format, args...), "")
	return ge
}

func AbortedErrorf(format string, args ...any) error {
	ge := newGRPCError(codes.Aborted, fmt.Errorf(format, args...), "")
	return ge
}

func InternalErrorf(format string, args ...any) error {
	ge := newGRPCError(codes.Internal, fmt.Errorf(format, args...), "")
	return ge
}
