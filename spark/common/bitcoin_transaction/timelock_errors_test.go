package bitcointransaction_test

import (
	"testing"

	"github.com/lightsparkdev/spark"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateSequenceFloorReturnsLeafRenewalRequired(t *testing.T) {
	_, err := bitcointransaction.ValidateSequence(spark.TimeLockInterval, bitcointransaction.TxTypeRefundCPFP, 0)
	require.Error(t, err)
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	assert.Equal(t, codes.InvalidArgument, code)
	assert.Equal(t, sparkerrors.ReasonInvalidArgumentLeafRenewalRequired, reason)
}

func TestValidateSequenceMismatchReturnsTimelockMismatch(t *testing.T) {
	// Current timelock 1000 expects the next refund at 900; provide 800.
	_, err := bitcointransaction.ValidateSequence(testTimeLock, bitcointransaction.TxTypeRefundCPFP, 800)
	require.Error(t, err)
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	assert.Equal(t, codes.InvalidArgument, code)
	assert.Equal(t, sparkerrors.ReasonInvalidArgumentTimelockMismatch, reason)

	md := errorInfoMetadataFrom(t, err)
	assert.Equal(t, "900", md[sparkerrors.ErrorMetadataExpectedTimelock])
	assert.Equal(t, "800", md[sparkerrors.ErrorMetadataProvidedTimelock])
}

func TestNextSequenceFloorReturnsLeafRenewalRequired(t *testing.T) {
	_, _, err := bitcointransaction.NextSequence(spark.TimeLockInterval)
	require.Error(t, err)
	_, reason := sparkerrors.CodeAndReasonFrom(err)
	assert.Equal(t, sparkerrors.ReasonInvalidArgumentLeafRenewalRequired, reason)
}

func errorInfoMetadataFrom(t *testing.T, err error) map[string]string {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok)
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			return ei.GetMetadata()
		}
	}
	t.Fatal("error has no ErrorInfo detail")
	return nil
}
