package grpc

import (
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
	return status.Error(e.Code, e.Cause.Error()).Error()
}

func (e *grpcError) Unwrap() error {
	return e.Cause
}

// wrapWithGRPCError wraps a response and an error into a gRPC error
func wrapWithGRPCError[T any](resp T, err error) (T, error) {
	if err != nil {
		return resp, toGRPCError(err)
	}
	return resp, nil
}

// toGRPCError converts any error to an appropriate gRPC error
func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	if grpcErr, ok := err.(Error); ok {
		return grpcErr.ToGRPCError()
	}

	// Default to Internal error
	return newGRPCError(codes.Internal, err)
}
