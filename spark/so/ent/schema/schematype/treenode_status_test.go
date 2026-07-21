package schematype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTreeNodeStatusIsTerminal(t *testing.T) {
	terminal := map[TreeNodeStatus]bool{
		TreeNodeStatusSplitted:   true,
		TreeNodeStatusAggregated: true,
		TreeNodeStatusExited:     true,
		TreeNodeStatusReimbursed: true,
	}
	for _, v := range (TreeNodeStatus("")).Values() {
		s := TreeNodeStatus(v)
		assert.Equal(t, terminal[s], s.IsTerminal(), "IsTerminal(%s)", s)
	}
	// ON_CHAIN and PARENT_EXITED are deliberately non-terminal for occupancy:
	// the watchtower still owes broadcast/refund work and rows drain to EXITED.
	assert.False(t, TreeNodeStatusOnChain.IsTerminal())
	assert.False(t, TreeNodeStatusParentExited.IsTerminal())
	// AGGREGATED is deliberately terminal for occupancy even though
	// CanBecomeAvailable() allows revival: it is consumed history in practice.
	assert.True(t, TreeNodeStatusAggregated.IsTerminal())
}

func TestOccupancyTreeNodeStatuses_ExcludesTerminalAndAvailable(t *testing.T) {
	occupancy := OccupancyTreeNodeStatuses()
	assert.Len(t, occupancy, len((TreeNodeStatus("")).Values())-6)
	for _, s := range occupancy {
		assert.True(t, s.CountsForOccupancy())
		assert.False(t, s.IsTerminal())
	}
	// AVAILABLE is non-terminal yet excluded: it is the resting spendable
	// pool, not in-flight work.
	assert.False(t, TreeNodeStatusAvailable.CountsForOccupancy())
	assert.NotContains(t, occupancy, TreeNodeStatusAvailable)
	// SPLIT_LOCKED is non-terminal yet excluded: renew-created split nodes
	// rest there permanently, so counting it would grow forever by design.
	assert.False(t, TreeNodeStatusSplitLocked.CountsForOccupancy())
	assert.NotContains(t, occupancy, TreeNodeStatusSplitLocked)
	// PARENT_EXITED counts: the watchtower still owes those children their
	// own broadcast work during unilateral exits.
	assert.Contains(t, occupancy, TreeNodeStatusParentExited)
	// LOST stays counted: it is the anomaly pool the growth alert watches.
	assert.Contains(t, occupancy, TreeNodeStatusLost)
	assert.Contains(t, occupancy, TreeNodeStatusOnChain)
	assert.NotContains(t, occupancy, TreeNodeStatusAggregated)
}
