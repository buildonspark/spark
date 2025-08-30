package errors

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error represents an error that can be converted to a gRPC error
type Error interface {
	error
	ToGRPCError() error
}

// grpcError resembles grpc's status.Error but it retains the original
// error cause such that functions up the stack can inspect it with
// errors.Unwrap() or errors.Is().
type grpcError struct {
	Code  codes.Code
	Cause error
}

// newGRPCError creates a new gRPC error with the given code and cause
func newGRPCError(code codes.Code, cause error) *grpcError {
	return &grpcError{
		Code:  code,
		Cause: cause,
	}
}

func (e *grpcError) Error() string {
	return e.Cause.Error()
}

func (e *grpcError) Unwrap() error {
	return e.Cause
}

// GRPCStatus is important so that when we return a grpcError, the gRPC
// server can infer the proper status from it.
// Docs: https://pkg.go.dev/google.golang.org/grpc/status#FromError
func (e *grpcError) GRPCStatus() *status.Status {
	return status.New(e.Code, e.Cause.Error())
}

// asGRPCError converts an error into a gRPC error.
// If there is an error in the chain that explicitly converts to a gRPC error, that error will be returned as is.
// If there is a grpcError in the error chain, that error code will be preserved and applied to the outermost error and the whole chain will be returned.
// Otherwise the error will be wrapped as an Internal error.
func asGRPCError(err error) error {
	if err != nil {
		return toGRPCError(err)
	}
	return nil
}

// toGRPCError converts any error to an appropriate gRPC error
func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	var convertable Error
	if errors.As(err, &convertable) {
		return convertable.ToGRPCError()
	}

	var grpcErr *grpcError
	if errors.As(err, &grpcErr) {
		return newGRPCError(grpcErr.Code, err)
	}

	// Default to Internal error
	return newGRPCError(codes.Internal, err)
}

// Error for when a concurrency limit is exceeded (e.g. too many concurrent requests)
func ConcurrencyLimitExceededError() error {
	return newGRPCError(codes.ResourceExhausted, fmt.Errorf("concurrency limit exceeded"))
}

func RateLimitExceededError() error {
	return newGRPCError(codes.ResourceExhausted, fmt.Errorf("rate limit exceeded"))
}

func InvalidUserInputErrorf(format string, args ...any) error {
	return newGRPCError(codes.InvalidArgument, fmt.Errorf(format, args...))
}

func FailedPreconditionErrorf(format string, args ...any) error {
	return newGRPCError(codes.FailedPrecondition, fmt.Errorf(format, args...))
}

func NotFoundErrorf(format string, args ...any) error {
	return newGRPCError(codes.NotFound, fmt.Errorf(format, args...))
}

func UnavailableErrorf(format string, args ...any) error {
	return newGRPCError(codes.Unavailable, fmt.Errorf(format, args...))
}

func AlreadyExistsErrorf(format string, args ...any) error {
	return newGRPCError(codes.AlreadyExists, fmt.Errorf(format, args...))
}

func UnauthenticatedError(format string, args ...any) error {
	return newGRPCError(codes.Unauthenticated, fmt.Errorf(format, args...))
}

func AlreadyExistsError(err error) error {
	return newGRPCError(codes.AlreadyExists, err)
}

func AbortedError(err error) error {
	return newGRPCError(codes.Aborted, err)
}

func AbortedErrorf(format string, args ...any) error {
	return newGRPCError(codes.Aborted, fmt.Errorf(format, args...))
}

func WrapErrorWithGRPCCode(err error, originalGRPCCode codes.Code) error {
	return newGRPCError(originalGRPCCode, err)
}

// WrapGRPCErrorWithMessage wraps an original gRPC error with a new message and
// preserves the original gRPC error code.
// If the original error is not a gRPC error, it will be wrapped as an Internal error.
func WrapGRPCErrorWithMessage(originalGRPCError error, message string) error {
	// Check if this is already a gRPC error that should be preserved
	if stat, ok := status.FromError(originalGRPCError); ok {
		// Preserve all gRPC error codes while adding context
		return WrapErrorWithGRPCCode(fmt.Errorf("%s: %w", message, originalGRPCError), stat.Code())
	}

	return newGRPCError(codes.Internal, fmt.Errorf("%s: %w", message, originalGRPCError))
}

func UnimplementedErrorf(format string, args ...any) error {
	return newGRPCError(codes.Unimplemented, fmt.Errorf(format, args...))
}
