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

// Both decision payloads take their target from a coordinator-supplied leaf id,
// so this comparison is all that stands between a legitimate prepared flow and a
// decision retargeted at someone else's leaf.
func TestRecoverWatchtowerExitedLeafValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewRecoverWatchtowerExitedLeafFlowHandler(sparktesting.TestConfig(t))
	leafID := uuid.New().String()
	prepare := &pbinternal.RecoverWatchtowerExitedLeafPrepareRequest{
		OriginalRequest: &pb.RecoverWatchtowerExitedLeafRequest{LeafId: strings.ToUpper(leafID)}, // non-canonical
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.RecoverWatchtowerExitedLeafCommitRequest{LeafId: leafID}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.RecoverWatchtowerExitedLeafRollbackRequest{LeafId: leafID}))
	// The reconciler's presumed-abort path echoes the prepare op itself. Compared
	// against a separate payload rather than against `prepare`, so this proves the
	// extraction and canonicalization rather than object identity.
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.RecoverWatchtowerExitedLeafPrepareRequest{
		OriginalRequest: &pb.RecoverWatchtowerExitedLeafRequest{LeafId: leafID},
	}))

	// A decision naming a different leaf, on each shape it can arrive in.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.RecoverWatchtowerExitedLeafCommitRequest{LeafId: uuid.NewString()}), "but this operator prepared leaf")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.RecoverWatchtowerExitedLeafRollbackRequest{LeafId: uuid.NewString()}), "but this operator prepared leaf")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.RecoverWatchtowerExitedLeafPrepareRequest{
		OriginalRequest: &pb.RecoverWatchtowerExitedLeafRequest{LeafId: uuid.NewString()},
	}), "but this operator prepared leaf")

	// Empty ids must not satisfy the fence by reading equal to each other. A
	// string comparison would let a decision carrying no leaf id through against a
	// prepare payload carrying no original request.
	require.Error(t, handler.ValidateDecisionAgainstPrepare(
		&pbinternal.RecoverWatchtowerExitedLeafPrepareRequest{},
		&pbinternal.RecoverWatchtowerExitedLeafCommitRequest{}))
	require.Error(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.RecoverWatchtowerExitedLeafCommitRequest{}))
	require.Error(t, handler.ValidateDecisionAgainstPrepare(
		&pbinternal.RecoverWatchtowerExitedLeafPrepareRequest{},
		&pbinternal.RecoverWatchtowerExitedLeafCommitRequest{LeafId: leafID}))

	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferPrepareRequest{}, &pbinternal.RecoverWatchtowerExitedLeafCommitRequest{}), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")
}
