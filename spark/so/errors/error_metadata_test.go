package errors

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func errorInfoFromStatus(t *testing.T, st *status.Status) *errdetails.ErrorInfo {
	t.Helper()
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			return ei
		}
	}
	t.Fatal("status has no ErrorInfo detail")
	return nil
}

func TestLeafRenewalRequiredCarriesReason(t *testing.T) {
	err := InvalidArgumentLeafRenewalRequired(fmt.Errorf("timelock at floor"))

	code, reason := CodeAndReasonFrom(err)
	assert.Equal(t, codes.InvalidArgument, code)
	assert.Equal(t, ReasonInvalidArgumentLeafRenewalRequired, reason)
}

func TestTimelockMismatchCarriesReasonAndTimelockMetadata(t *testing.T) {
	err := InvalidArgumentTimelockMismatch(fmt.Errorf("timelock mismatch"), 900, 800)

	code, reason := CodeAndReasonFrom(err)
	assert.Equal(t, codes.InvalidArgument, code)
	assert.Equal(t, ReasonInvalidArgumentTimelockMismatch, reason)

	st, ok := status.FromError(err)
	require.True(t, ok)
	info := errorInfoFromStatus(t, st)
	assert.Equal(t, "900", info.GetMetadata()[ErrorMetadataExpectedTimelock])
	assert.Equal(t, "800", info.GetMetadata()[ErrorMetadataProvidedTimelock])
}

func TestWrapErrorWithMetadataMergesAndPreservesReason(t *testing.T) {
	inner := InvalidArgumentTimelockMismatch(fmt.Errorf("timelock mismatch"), 900, 800)
	wrapped := WrapErrorWithMetadata(
		fmt.Errorf("failed to verify transaction of leaf leaf-9: %w", inner),
		map[string]string{ErrorMetadataLeafID: "leaf-9"},
	)

	code, reason := CodeAndReasonFrom(wrapped)
	assert.Equal(t, codes.InvalidArgument, code)
	assert.Equal(t, ReasonInvalidArgumentTimelockMismatch, reason)
	assert.Equal(t, "failed to verify transaction of leaf leaf-9: timelock mismatch", wrapped.Error())

	st, ok := status.FromError(wrapped)
	require.True(t, ok)
	info := errorInfoFromStatus(t, st)
	assert.Equal(t, "leaf-9", info.GetMetadata()[ErrorMetadataLeafID])
	assert.Equal(t, "900", info.GetMetadata()[ErrorMetadataExpectedTimelock])
	assert.Equal(t, "800", info.GetMetadata()[ErrorMetadataProvidedTimelock])
}

func TestWrapErrorWithMetadataIsANoOpWithoutReason(t *testing.T) {
	plain := fmt.Errorf("plain error")
	wrapped := WrapErrorWithMetadata(plain, map[string]string{ErrorMetadataLeafID: "leaf-1"})
	assert.Equal(t, plain, wrapped)
}

func TestWrapErrorWithMessageAndMetadataPreservesAcrossStatusRoundTrip(t *testing.T) {
	// Simulates a remote peer's rejection arriving as a plain gRPC status
	// error (e.g. a 2PC Prepare response) and being re-wrapped locally.
	inner := InvalidArgumentLeafRenewalRequired(fmt.Errorf("timelock at floor"))
	innerStatus, ok := status.FromError(inner)
	require.True(t, ok)
	remote := innerStatus.Err()

	wrapped := WrapErrorWithMessageAndMetadata(
		remote, "consensus send transfer failed",
		map[string]string{ErrorMetadataLeafID: "leaf-42"},
	)

	code, reason := CodeAndReasonFrom(wrapped)
	assert.Equal(t, codes.InvalidArgument, code)
	assert.Equal(t, ReasonInvalidArgumentLeafRenewalRequired, reason)
	assert.Contains(t, wrapped.Error(), "consensus send transfer failed: ")
	assert.Contains(t, wrapped.Error(), "timelock at floor")

	st, ok := status.FromError(wrapped)
	require.True(t, ok)
	info := errorInfoFromStatus(t, st)
	assert.Equal(t, "leaf-42", info.GetMetadata()[ErrorMetadataLeafID])
}

func TestWrapErrorWithCodeClearsReasonAndMetadata(t *testing.T) {
	inner := WrapErrorWithMetadata(
		InvalidArgumentLeafRenewalRequired(fmt.Errorf("timelock at floor")),
		map[string]string{ErrorMetadataLeafID: "leaf-1"},
	)
	wrapped := WrapErrorWithCode(inner, codes.Internal)

	code, reason := CodeAndReasonFrom(wrapped)
	assert.Equal(t, codes.Internal, code)
	assert.Empty(t, reason)

	st, ok := status.FromError(wrapped)
	require.True(t, ok)
	assert.Empty(t, st.Details())
}

func TestHasReason(t *testing.T) {
	assert.True(t, HasReason(InvalidArgumentLeafRenewalRequired(fmt.Errorf("at floor"))))
	assert.True(t, HasReason(fmt.Errorf("context: %w", InvalidArgumentMalformedField(fmt.Errorf("bad field")))))
	assert.False(t, HasReason(fmt.Errorf("plain error")))
}
