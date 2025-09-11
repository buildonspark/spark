package errors

import (
	"errors"
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
	Code   codes.Code
	Cause  error
	Reason string
}

// newGRPCError creates a new gRPC error with the given code and cause
func newGRPCError(code codes.Code, cause error, reason string) *grpcError {
	return &grpcError{
		Code:   code,
		Cause:  cause,
		Reason: reason,
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
	st := status.New(e.Code, e.Cause.Error())
	if e.Reason != "" {
		if stWith, err := st.WithDetails(&errdetails.ErrorInfo{Reason: e.Reason}); err == nil {
			st = stWith
		}
	}
	return st
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
		return &grpcError{
			Code:   grpcErr.Code,
			Cause:  err,
			Reason: grpcErr.Reason,
		}
	}

	// Preserve existing gRPC status errors (and their details/reason) as-is.
	if st, ok := status.FromError(err); ok {
		if st.Code() == codes.OK {
			return nil
		}
		return err
	}

	// Default to Internal error with no reason.
	return &grpcError{Code: codes.Internal, Cause: err, Reason: ""}
}

// WrapErrorWithCode should be used to convert a standard Go error into a gRPC error with a specific code.
// The original error will be used as the message.
func WrapErrorWithCode(err error, grpcCode codes.Code) error {
	return wrapGRPC(err, &grpcCode, nil, "")
}

// WrapErrorWithCodeAndReason should be used to convert a standard Go error into a gRPC error with a specific code and a
// machine-readable reason. The original error will be used as the message.
func WrapErrorWithCodeAndReason(err error, grpcCode codes.Code, reason string) error {
	return wrapGRPC(err, &grpcCode, &reason, "")
}

// WrapErrorWithMessage should be used to add a more descriptive, human-readable message to an existing gRPC error.
// The original gRPC code and reason will be preserved.
func WrapErrorWithMessage(orig error, message string) error {
	return wrapGRPC(orig, nil, nil, message)
}

// WrapErrorWithReasonPrefix should be used when an error is returned from an external service (e.g., another coordinator)
// to add context about the source of the error. The original gRPC code and message are preserved, but the reason is
// prefixed to identify where the error originated.
func WrapErrorWithReasonPrefix(err error, prefix string) error {
	if err == nil {
		return nil
	}
	code, reason := CodeAndReasonFrom(err)
	if prefix != "" {
		if reason == "" {
			// No reason, so just use the prefix.
			reason = fmt.Sprintf("%s", prefix)
		} else {
			reason = fmt.Sprintf("%s:%s", prefix, reason)
		}
	}
	return &grpcError{Code: code, Cause: err, Reason: reason}
}

func CodeAndReasonFrom(err error) (codes.Code, string) {
	var ge *grpcError
	if errors.As(err, &ge) {
		return ge.Code, ge.Reason
	}
	// The code and reason could be set either via the grpcError (our definition) or via the gRPC (standardized) status details.
	if st, ok := status.FromError(err); ok {
		code := st.Code()
		var reason string
		for _, d := range st.Details() {
			if ei, ok := d.(*errdetails.ErrorInfo); ok && ei.Reason != "" {
				reason = ei.Reason
				break
			}
		}
		return code, reason
	}
	return codes.Internal, ""
}

func wrapGRPC(err error, codeOverride *codes.Code, reasonOverride *string, msg string) error {
	if err == nil {
		return nil
	}
	code, reason := CodeAndReasonFrom(err)
	if codeOverride != nil {
		code = *codeOverride
		if reasonOverride == nil {
			reason = ""
		}
	}
	if reasonOverride != nil {
		reason = *reasonOverride
	}
	cause := err
	if msg != "" {
		cause = fmt.Errorf("%s: %w", msg, err)
	}
	return &grpcError{Code: code, Cause: cause, Reason: reason}
}
