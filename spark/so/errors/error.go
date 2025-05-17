package errors

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

// This is important so that when we return a grpcError, the gRPC
// server can infer the proper status from it.
// Docs: https://pkg.go.dev/google.golang.org/grpc/status#FromError
func (e *grpcError) GRPCStatus() *status.Status {
	return status.New(e.Code, e.Cause.Error())
}

// wrapWithGRPCError wraps a response and an error into a gRPC error
func WrapWithGRPCError[T proto.Message](resp T, err error) (T, error) {
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

	var grpcErr *grpcError
	if errors.As(err, &grpcErr) {
		return grpcErr
	}

	// Default to Internal error
	return newGRPCError(codes.Internal, err)
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
