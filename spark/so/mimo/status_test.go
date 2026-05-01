package mimo_test

import (
	"testing"

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
