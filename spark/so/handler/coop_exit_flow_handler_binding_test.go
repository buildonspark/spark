package handler

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

// TestCoopExitValidateDecisionAgainstPrepare directly covers the COOP_EXIT
// binding fence, whose prepared id is one level deeper
// (OriginalRequest.Transfer.TransferId) — the extra nesting a copy/paste is
// most likely to get wrong.
func TestCoopExitValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewCoopExitFlowHandler(sparktesting.TestConfig(t))
	transferID := uuid.New().String()
	prepare := &pbinternal.CoopExitPrepareRequest{
		OriginalRequest: &pb.CooperativeExitRequest{
			Transfer: &pb.StartTransferRequest{TransferId: strings.ToUpper(transferID)}, // non-canonical
		},
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.CoopExitCommitRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.CoopExitRollbackRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, prepare))
	// Presumed-abort accept path with a SEPARATE prepare-shape decision carrying a
	// canonically-equal (non-canonical) id — proves the deep-nested extraction
	// (OriginalRequest.Transfer.TransferId) and UUID canonicalization actually
	// work on that branch, not just an object-identity match against itself.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.CoopExitPrepareRequest{
		OriginalRequest: &pb.CooperativeExitRequest{Transfer: &pb.StartTransferRequest{TransferId: transferID}},
	}))

	// Wrong transfer id on both copy-pasted decision variants.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.CoopExitCommitRequest{TransferId: uuid.NewString()}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.CoopExitRollbackRequest{TransferId: uuid.NewString()}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferPrepareRequest{}, &pbinternal.CoopExitCommitRequest{}), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")

	// Presumed-abort path (prepare-shape decision) with the deep nesting must
	// still reject a mismatch.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.CoopExitPrepareRequest{
		OriginalRequest: &pb.CooperativeExitRequest{Transfer: &pb.StartTransferRequest{TransferId: uuid.NewString()}},
	}), "does not match the prepared transfer id")
}
