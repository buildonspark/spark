package errors

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	msg   = "message with sensitive data"
	okVal = "ok"
)

var (
	grpcErr = func() error {
		st, _ := status.New(codes.Internal, msg).WithDetails(&errdetails.ErrorInfo{Reason: ReasonInternalDependencyFailure})
		return st.Err()
	}()
	errHandler = func(_ context.Context, _ any) (any, error) {
		return nil, grpcErr
	}
)

func TestErrorInterceptor_NoError_ReturnsValue(t *testing.T) {
	serverInfo := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/SomeMethod"}
	okHandler := func(_ context.Context, _ any) (any, error) {
		return okVal, nil
	}

	got, err := ErrorInterceptor(true)(t.Context(), nil, serverInfo, okHandler)

	require.NoError(t, err)
	assert.Equal(t, okVal, got)
}

func TestInternalErrorDetailMasking(t *testing.T) {
	tests := []struct {
		name           string
		detailedErrors bool
		fullMethod     string
		wantDetails    bool
		handler        func(_ context.Context, _ any) (any, error)
		expectedCode   codes.Code
		expectedReason string
	}{
		{
			name:           "mask details if detailedErrors disabled",
			detailedErrors: false,
			fullMethod:     "/spark.SparkService/SomeMethod",
			wantDetails:    false,
			handler:        errHandler,
			expectedCode:   codes.Internal,
			expectedReason: "",
		},
		{
			name:           "show details if detailedErrors enabled",
			detailedErrors: true,
			fullMethod:     "/spark.SparkService/SomeMethod",
			wantDetails:    true,
			handler:        errHandler,
			expectedCode:   codes.Internal,
			expectedReason: ReasonInternalDependencyFailure,
		},
		{
			name:           "show details for internal service",
			detailedErrors: true,
			fullMethod:     "/spark_internal.SparkInternalService/SomeMethod",
			wantDetails:    true,
			handler:        errHandler,
			expectedCode:   codes.Internal,
			expectedReason: ReasonInternalDependencyFailure,
		},
		{
			name:           "show details for failed precondition error",
			detailedErrors: false,
			fullMethod:     "/spark_internal.SparkInternalService/SomeMethod",
			wantDetails:    false,
			handler: func(_ context.Context, _ any) (any, error) {
				return nil, FailedPreconditionNotSpendable(fmt.Errorf("utxo not found: txid: %s vout: %d", "492aef9399b186196effc64f5a754fb6ccff5942adefde9786da5249eb9dde47", 0))
			},
			expectedCode:   codes.FailedPrecondition,
			expectedReason: "NOT_SPENDABLE",
		},
		{
			name:           "show details for internal service even if detailedErrors disabled",
			detailedErrors: false,
			fullMethod:     "/spark_internal.SparkInternalService/SomeMethod",
			wantDetails:    true,
			handler:        errHandler,
			expectedCode:   codes.Internal,
			expectedReason: ReasonInternalDependencyFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverInfo := &grpc.UnaryServerInfo{FullMethod: tt.fullMethod}
			_, err := ErrorInterceptor(tt.detailedErrors)(t.Context(), nil, serverInfo, tt.handler)
			require.Error(t, err)
			st := status.Convert(err)
			if tt.expectedCode == codes.Internal {
				if tt.wantDetails {
					require.ErrorContains(t, err, msg)
					assert.GreaterOrEqual(t, len(st.Details()), 1)
				} else {
					require.NotContains(t, err.Error(), msg)
					assert.Empty(t, st.Details())
				}
			}
			require.Equal(t, tt.expectedCode, status.Convert(err).Code())

			code, reason := CodeAndReasonFrom(err)
			assert.Equal(t, tt.expectedCode, code)
			assert.Equal(t, tt.expectedReason, reason)
		})
	}
}

func TestAsGRPCError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantErr     bool
		wantErrCode codes.Code
	}{
		{
			name:        "no error returns response and nil",
			err:         nil,
			wantErr:     false,
			wantErrCode: codes.OK,
		},
		{
			name:        "with error returns response and wrapped error",
			err:         fmt.Errorf("test error"),
			wantErr:     true,
			wantErrCode: codes.Internal,
		},
		{
			name:        "with custom error returns response and custom error",
			err:         &fakeError{message: "custom error", grpcErr: status.Error(codes.InvalidArgument, "custom")},
			wantErr:     true,
			wantErrCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := asGRPCError(tt.err)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantErrCode, status.Convert(err).Code())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestAsGRPCError_PropagatesErrorCode(t *testing.T) {
	abortedErr := AbortedTransactionPreempted(fmt.Errorf("inner aborted error"))
	wrappedErr := fmt.Errorf("wrapped error: %w", abortedErr)
	err := asGRPCError(wrappedErr)
	require.Error(t, err)
	assert.Equal(t, codes.Aborted, status.Convert(err).Code())
	assert.Equal(t, "wrapped error: inner aborted error", err.Error())
}

func TestToGRPCError_NilError_ReturnsNil(t *testing.T) {
	require.NoError(t, toGRPCError(nil))
}

func TestToGRPCError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantErrCode codes.Code
		wantMessage string
	}{
		{
			name:        "regular error returns internal error",
			err:         fmt.Errorf("test error"),
			wantErrCode: codes.Internal,
			wantMessage: "test error",
		},
		{
			name:        "custom error returns its gRPC error",
			err:         &fakeError{message: "custom", grpcErr: status.Error(codes.InvalidArgument, "custom grpc")},
			wantErrCode: codes.InvalidArgument,
			wantMessage: "custom grpc",
		},
		{
			name:        "existing grpcError returns same error",
			err:         InvalidUserInputErrorf("not found"),
			wantErrCode: codes.InvalidArgument,
			wantMessage: "not found",
		},
		{
			name:        "not found error returns not found code",
			err:         NotFoundErrorf("resource not found"),
			wantErrCode: codes.NotFound,
			wantMessage: "resource not found",
		},
		{
			name:        "failed precondition error returns failed precondition code",
			err:         FailedPreconditionErrorf("precondition failed"),
			wantErrCode: codes.FailedPrecondition,
			wantMessage: "precondition failed",
		},
		{
			name:        "unavailable error returns unavailable code",
			err:         UnavailableErrorf("service unavailable"),
			wantErrCode: codes.Unavailable,
			wantMessage: "service unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := toGRPCError(tt.err)

			require.Error(t, err)
			st := status.Convert(err)
			assert.Equal(t, tt.wantErrCode, st.Code())
			assert.Equal(t, tt.wantMessage, st.Message())
		})
	}
}

func TestCodeAndReasonFrom_InternalError_NoDefaultReason(t *testing.T) {
	err := newGRPCError(codes.InvalidArgument, errors.New("boom"), "")
	code, reason := CodeAndReasonFrom(err)
	assert.Equal(t, codes.InvalidArgument, code)
	assert.Empty(t, reason)
}

func TestCodeAndReasonFrom_InternalError_ExplicitReason(t *testing.T) {
	err := newGRPCError(codes.Aborted, errors.New("x"), ReasonAbortedTransactionPreempted)
	code, reason := CodeAndReasonFrom(err)
	assert.Equal(t, codes.Aborted, code)
	assert.Equal(t, ReasonAbortedTransactionPreempted, reason)
}

func TestCodeAndReasonFrom_UpstreamStatus_ErrorInfo(t *testing.T) {
	st, _ := status.New(codes.FailedPrecondition, "oops").WithDetails(&errdetails.ErrorInfo{Reason: ReasonFailedPreconditionNotSpendable})
	code, reason := CodeAndReasonFrom(st.Err())
	assert.Equal(t, codes.FailedPrecondition, code)
	assert.Equal(t, ReasonFailedPreconditionNotSpendable, reason)
}

func TestCodeAndReasonFrom_StatusWithoutErrorInfo_NoDefaultReason(t *testing.T) {
	st := status.New(codes.Unavailable, "downstream down")
	code, reason := CodeAndReasonFrom(st.Err())
	assert.Equal(t, codes.Unavailable, code)
	assert.Empty(t, reason)
}

func TestWrapGRPC_WithMessage_PreservesCodeAndReason(t *testing.T) {
	base := newGRPCError(codes.FailedPrecondition, errors.New("bad sig"), ReasonFailedPreconditionBadSignature)
	wrapped := WrapErrorWithMessage(base, "while verifying")
	code, reason := CodeAndReasonFrom(wrapped)
	assert.Equal(t, codes.FailedPrecondition, code)
	assert.Equal(t, ReasonFailedPreconditionBadSignature, reason)
	assert.Equal(t, "while verifying: bad sig", wrapped.Error())
}

func TestWrapGRPC_CodeOverride_ResetsReason(t *testing.T) {
	base := newGRPCError(codes.Aborted, errors.New("x"), ReasonAbortedTransactionPreempted)
	wrapped := WrapErrorWithCode(base, codes.NotFound)
	code, reason := CodeAndReasonFrom(wrapped)
	assert.Equal(t, codes.NotFound, code)
	assert.Empty(t, reason)
}

func TestWrapGRPC_CodeAndReasonOverride(t *testing.T) {
	base := newGRPCError(codes.Aborted, errors.New("x"), ReasonAbortedTransactionPreempted)
	wrapped := WrapErrorWithCodeAndReason(base, codes.NotFound, ReasonNotFoundMissingEntity)
	code, reason := CodeAndReasonFrom(wrapped)
	assert.Equal(t, codes.NotFound, code)
	assert.Equal(t, ReasonNotFoundMissingEntity, reason)
}

func TestGRPCStatus_AttachesErrorInfo(t *testing.T) {
	err := newGRPCError(codes.Unavailable, errors.New("db down"), ReasonResourceExhaustedConcurrencyLimitExceeded)
	st := status.Convert(err)
	var gotReason string
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			gotReason = ei.Reason
			break
		}
	}
	assert.Equal(t, ReasonResourceExhaustedConcurrencyLimitExceeded, gotReason)
}

func TestInvalidUserInputErrorf(t *testing.T) {
	err := InvalidUserInputErrorf("invalid input: %s, value: %d", "field", 42)

	require.Error(t, err)
	st := status.Convert(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Equal(t, "invalid input: field, value: 42", st.Message())
}

func TestFailedPreconditionErrorf(t *testing.T) {
	err := FailedPreconditionErrorf("precondition failed: %s, state: %s", "operation", "pending")

	require.Error(t, err)
	st := status.Convert(err)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Equal(t, "precondition failed: operation, state: pending", st.Message())
}

func TestNotFoundErrorf(t *testing.T) {
	err := NotFoundErrorf("resource not found: %s with id %d", "user", 123)

	require.Error(t, err)
	st := status.Convert(err)
	assert.Equal(t, codes.NotFound, st.Code())
	assert.Equal(t, "resource not found: user with id 123", st.Message())
}

func TestUnavailableErrorf(t *testing.T) {
	err := UnavailableErrorf("service unavailable: %s, retry after %d seconds", "database", 30)

	require.Error(t, err)
	st := status.Convert(err)
	assert.Equal(t, codes.Unavailable, st.Code())
	assert.Equal(t, "service unavailable: database, retry after 30 seconds", st.Message())
}

func TestReasonConstructor_ResourceExhaustedRateLimitExceeded(t *testing.T) {
	err := ResourceExhaustedRateLimitExceeded(fmt.Errorf("rate limit exceeded"))
	st := status.Convert(err)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
	assert.Equal(t, "rate limit exceeded", st.Message())
	var gotReason string
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			gotReason = ei.Reason
			break
		}
	}
	assert.Equal(t, ReasonResourceExhaustedRateLimitExceeded, gotReason)
}

func TestWrapGRPCErrorWithReasonPrefix(t *testing.T) {
	t.Run("prefixes a standard grpc error", func(t *testing.T) {
		originalErr := status.Error(codes.Unavailable, "downstream is down")
		wrappedErr := WrapErrorWithReasonPrefix(originalErr, "FAILED_WITH_EXTERNAL_COORDINATOR")

		require.Error(t, wrappedErr)
		st := status.Convert(wrappedErr)
		assert.Equal(t, codes.Unavailable, st.Code())
		assert.Equal(t, originalErr.Error(), st.Message())

		code, reason := CodeAndReasonFrom(wrappedErr)
		assert.Equal(t, codes.Unavailable, code)
		assert.Equal(t, "FAILED_WITH_EXTERNAL_COORDINATOR", reason)
	})

	t.Run("prefixes an error with existing reason", func(t *testing.T) {
		originalErr := FailedPreconditionNotSpendable(errors.New("already spent"))
		wrappedErr := WrapErrorWithReasonPrefix(originalErr, "FAILED_WITH_EXTERNAL_COORDINATOR")

		require.Error(t, wrappedErr)
		st := status.Convert(wrappedErr)
		assert.Equal(t, codes.FailedPrecondition, st.Code())
		assert.Equal(t, "already spent", st.Message())

		code, reason := CodeAndReasonFrom(wrappedErr)
		assert.Equal(t, codes.FailedPrecondition, code)
		assert.Equal(t, "FAILED_WITH_EXTERNAL_COORDINATOR:NOT_SPENDABLE", reason)
	})

	t.Run("wraps a non-grpc error", func(t *testing.T) {
		originalErr := errors.New("a plain error")
		wrappedErr := WrapErrorWithReasonPrefix(originalErr, "FAILED_WITH_EXTERNAL_COORDINATOR")

		require.Error(t, wrappedErr)
		st := status.Convert(wrappedErr)
		assert.Equal(t, codes.Internal, st.Code())
		assert.Equal(t, "a plain error", st.Message())

		code, reason := CodeAndReasonFrom(wrappedErr)
		assert.Equal(t, codes.Internal, code)
		assert.Equal(t, "FAILED_WITH_EXTERNAL_COORDINATOR", reason)
	})

	t.Run("handles nil error", func(t *testing.T) {
		wrappedErr := WrapErrorWithReasonPrefix(nil, "FAILED_WITH_EXTERNAL_COORDINATOR")
		assert.NoError(t, wrappedErr)
	})
}

// fakeError is an Error interface implementation for testing.
type fakeError struct {
	message string
	grpcErr error
}

func (m *fakeError) Error() string {
	return m.message
}

func (m *fakeError) ToGRPCError() error {
	return m.grpcErr
}
