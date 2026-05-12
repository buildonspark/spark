package mimo_test

import (
	"testing"

	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/mimo"
	"github.com/stretchr/testify/assert"
)

// TestPendingStatusesDisjoint locks the invariant that
// PendingSenderStatuses and PendingReceiverStatuses don't overlap.
//
// buildPendingIDsQuerySenderOrReceiver in transfer_handler.go relies on
// this disjointness to skip DISTINCT on the UNION ALL — if a future change
// breaks the invariant (adds a status to both, or adds a state-machine
// path that produces a row satisfying both filters), SR1 silently emits
// duplicates. See the comment block on that function for the full
// invariant chain.
func TestPendingStatusesDisjoint(t *testing.T) {
	senderSet := make(map[string]bool, len(mimo.PendingSenderStatuses()))
	for _, s := range mimo.PendingSenderStatuses() {
		senderSet[s] = true
	}

	var overlap []string
	for _, r := range mimo.PendingReceiverStatuses() {
		if senderSet[r] {
			overlap = append(overlap, r)
		}
	}

	assert.Empty(t, overlap,
		"PendingSenderStatuses and PendingReceiverStatuses must be disjoint — "+
			"buildPendingIDsQuerySenderOrReceiver relies on this for correctness without DISTINCT")
}

// Locks the exact 4-state set. Drift without a matching index migration
// silently disables partial-index drive for the SDK's filter shape.
func TestOutgoingInFlightSenderStatuses(t *testing.T) {
	assert.ElementsMatch(t, []string{
		"SENDER_INITIATED",
		"SENDER_INITIATED_COORDINATOR",
		"APPLYING_SENDER_KEY_TWEAK",
		"SENDER_KEY_TWEAK_PENDING",
	}, mimo.OutgoingInFlightSenderStatuses())
}

// Locks the documented superset relationship between the two sender status sets.
func TestOutgoingInFlightIsSupersetOfPendingSender(t *testing.T) {
	outgoingSet := make(map[string]bool, len(mimo.OutgoingInFlightSenderStatuses()))
	for _, s := range mimo.OutgoingInFlightSenderStatuses() {
		outgoingSet[s] = true
	}
	for _, s := range mimo.PendingSenderStatuses() {
		assert.True(t, outgoingSet[s],
			"PendingSenderStatuses element %q must also be in OutgoingInFlightSenderStatuses", s)
	}
}

// Locks IsOutgoingInFlightStatus to the same 4-state set as
// OutgoingInFlightSenderStatuses — drift would silently break the
// QueryAllTransfers dispatcher's shape detection.
func TestIsOutgoingInFlightStatus(t *testing.T) {
	for _, s := range mimo.OutgoingInFlightSenderStatuses() {
		assert.Truef(t, mimo.IsOutgoingInFlightStatus(st.TransferStatus(s)),
			"IsOutgoingInFlightStatus(%q) should be true", s)
	}

	negatives := []st.TransferStatus{
		st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
		st.TransferStatusCompleted,
		st.TransferStatusExpired,
		st.TransferStatusReturned,
	}
	for _, s := range negatives {
		assert.Falsef(t, mimo.IsOutgoingInFlightStatus(s),
			"IsOutgoingInFlightStatus(%q) should be false", s)
	}
}
