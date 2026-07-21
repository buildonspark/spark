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

// TestInitiatePreimageSwapValidateDecisionAgainstPrepare directly covers the
// INITIATE_PREIMAGE_SWAP binding fence. The prepared id is resolved via
// preimageSwapTransferIDFromRequest (the transfer lives under transfer_request;
// the legacy transfer/V2 shape is disabled), UUID-aware so a non-canonical
// prepared id still matches the canonicalized decision id.
func TestInitiatePreimageSwapValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewInitiatePreimageSwapFlowHandler(sparktesting.TestConfig(t))
	transferID := uuid.New().String()
	prepare := &pbinternal.InitiatePreimageSwapPrepareRequest{
		OriginalRequest: &pb.InitiatePreimageSwapRequest{
			Reason:          pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			TransferRequest: &pb.StartTransferRequest{TransferId: strings.ToUpper(transferID)}, // non-canonical
		},
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiatePreimageSwapCommitRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: transferID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, prepare))

	// Wrong transfer id on both copy-pasted decision variants.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiatePreimageSwapCommitRequest{TransferId: uuid.NewString()}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: uuid.NewString()}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferPrepareRequest{}, &pbinternal.InitiatePreimageSwapCommitRequest{}), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")

	// Presumed-abort path (prepare-shape decision) must still reject a mismatch.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.InitiatePreimageSwapPrepareRequest{
		OriginalRequest: &pb.InitiatePreimageSwapRequest{
			Reason:          pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
			TransferRequest: &pb.StartTransferRequest{TransferId: uuid.NewString()},
		},
	}), "does not match the prepared transfer id")

	// SEND+package shape: the commit settles by applying aggregated HTLC refund
	// signatures, so an empty-leaf_signatures commit with a matching id must be
	// rejected — otherwise applyInitiatePreimageSwapCommit no-ops and marks the
	// row COMMITTED while the transfer stays SenderKeyTweakPending (stuck funds).
	sendPkgID := uuid.New().String()
	sendPkgPrepare := &pbinternal.InitiatePreimageSwapPrepareRequest{
		OriginalRequest: &pb.InitiatePreimageSwapRequest{
			Reason:          pb.InitiatePreimageSwapRequest_REASON_SEND,
			TransferRequest: &pb.StartTransferRequest{TransferId: sendPkgID},
		},
	}
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(sendPkgPrepare,
		&pbinternal.InitiatePreimageSwapCommitRequest{TransferId: sendPkgID}), "carries no leaf signatures")
	// A commit that carries leaf signatures passes.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(sendPkgPrepare, &pbinternal.InitiatePreimageSwapCommitRequest{
		TransferId:     sendPkgID,
		LeafSignatures: []*pbinternal.SendTransferLeafSignatures{{LeafId: uuid.NewString()}},
	}))
	// Rollback for the same shape does not require leaf signatures.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(sendPkgPrepare,
		&pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: sendPkgID}))

	// Degenerate prepare with a nil OriginalRequest resolves to an empty prepared
	// id, so any real decision id is a mismatch — fail closed, never a pass.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(
		&pbinternal.InitiatePreimageSwapPrepareRequest{},
		&pbinternal.InitiatePreimageSwapCommitRequest{TransferId: uuid.NewString()}),
		"does not match the prepared transfer id")

	// The leaf-signature bind is classified as !=REASON_RECEIVE, so an
	// unrecognized Reason (not RECEIVE) with a package is still subject to it: an
	// empty-leaf_signatures commit is rejected. (Prepare rejects such a Reason up
	// front — see TestInitiatePreimageSwapPrepareRejectsUnknownReason — so this
	// only guards the fence predicate itself.)
	unknownReasonID := uuid.New().String()
	unknownReasonPrepare := &pbinternal.InitiatePreimageSwapPrepareRequest{
		OriginalRequest: &pb.InitiatePreimageSwapRequest{
			Reason:          pb.InitiatePreimageSwapRequest_Reason(99),
			TransferRequest: &pb.StartTransferRequest{TransferId: unknownReasonID},
		},
	}
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(unknownReasonPrepare,
		&pbinternal.InitiatePreimageSwapCommitRequest{TransferId: unknownReasonID}), "carries no leaf signatures")
}

// TestInitiatePreimageSwapPrepareRejectsUnknownReason pins the Prepare-side
// guard: an unrecognized Reason is rejected before any work, so it can never be
// prepared-then-fenced-at-commit (the classifications gate on ==REASON_SEND for
// producing signatures vs !=REASON_RECEIVE for requiring them, which agree only
// when unknown reasons are excluded here).
func TestInitiatePreimageSwapPrepareRejectsUnknownReason(t *testing.T) {
	handler := NewInitiatePreimageSwapFlowHandler(sparktesting.TestConfig(t))
	_, err := handler.Prepare(t.Context(), &pbinternal.InitiatePreimageSwapPrepareRequest{
		OriginalRequest: &pb.InitiatePreimageSwapRequest{
			Reason:          pb.InitiatePreimageSwapRequest_Reason(99),
			TransferRequest: &pb.StartTransferRequest{TransferId: uuid.NewString()},
		},
	})
	require.ErrorContains(t, err, "unrecognized preimage swap reason")
}
