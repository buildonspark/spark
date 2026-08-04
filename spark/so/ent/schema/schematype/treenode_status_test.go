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

func TestTreeNodeStatusCanBecomeAvailable(t *testing.T) {
	revivable := map[TreeNodeStatus]bool{
		TreeNodeStatusCreating:       true,
		TreeNodeStatusAvailable:      true,
		TreeNodeStatusFrozenByIssuer: true,
		TreeNodeStatusTransferLocked: true,
		TreeNodeStatusSplitLocked:    true,
		TreeNodeStatusAggregated:     true,
		TreeNodeStatusAggregateLock:  true,
		TreeNodeStatusInvestigation:  true,
		TreeNodeStatusLost:           true,
		TreeNodeStatusRenewLocked:    true,
	}
	for _, v := range (TreeNodeStatus("")).Values() {
		s := TreeNodeStatus(v)
		assert.Equal(t, revivable[s], s.CanBecomeAvailable(), "CanBecomeAvailable(%s)", s)
	}
	// A consolidated node's value lives in the exit package on the node
	// itself; reviving it to AVAILABLE alongside its retired children would
	// let the same value be claimed twice (the SP-3049 class).
	assert.False(t, TreeNodeStatusConsolidated.CanBecomeAvailable())
	// AGGREGATE_LOCK must stay revivable: rollback restores locked nodes to
	// AVAILABLE.
	assert.True(t, TreeNodeStatusAggregateLock.CanBecomeAvailable())
}

func TestTreeNodeStatusShouldMarkParentExited(t *testing.T) {
	swept := map[TreeNodeStatus]bool{
		TreeNodeStatusCreating:       true,
		TreeNodeStatusAvailable:      true,
		TreeNodeStatusFrozenByIssuer: true,
		TreeNodeStatusTransferLocked: true,
		TreeNodeStatusSplitLocked:    true,
		TreeNodeStatusRenewLocked:    true,
		TreeNodeStatusInvestigation:  true,
		TreeNodeStatusLost:           true,
	}
	for _, v := range (TreeNodeStatus("")).Values() {
		s := TreeNodeStatus(v)
		assert.Equal(t, swept[s], s.ShouldMarkParentExited(), "ShouldMarkParentExited(%s)", s)
	}
	// Every status the sweep writes over must be one that could otherwise reach
	// AVAILABLE — that is the whole rule. REIMBURSED was the one exception and
	// is now excluded; this catches a new status being added to the swept arm
	// without checking it against that rule.
	for _, v := range (TreeNodeStatus("")).Values() {
		s := TreeNodeStatus(v)
		if s.ShouldMarkParentExited() {
			assert.True(t, s.CanBecomeAvailable(),
				"%s is swept to PARENT_EXITED but can never reach AVAILABLE, so the sweep only destroys a terminal marker", s)
		}
	}
	// A reimbursed node is settled and the write is one-way: PARENT_EXITED
	// satisfies IsExitedToL1() (which validateFinalizeTransferLeafCanComplete
	// gates on) and is not in the watchtower's terminal set, so sweeping would
	// re-admit a paid-out node to both.
	assert.False(t, TreeNodeStatusReimbursed.ShouldMarkParentExited())
	// A branch must keep SPLITTED: its parent confirming advances its own exit
	// rather than invalidating it, and PARENT_EXITED is non-terminal, so the
	// watchtower would retry its timelock-disabled tx on every scan tick.
	assert.False(t, TreeNodeStatusSplitted.ShouldMarkParentExited())
	// Renewal split nodes must be swept: staying non-terminal is what keeps
	// them inside the watchtower's work set.
	assert.True(t, TreeNodeStatusSplitLocked.ShouldMarkParentExited())
	// Transient lock states drain back to AVAILABLE, so skipping them would
	// reopen the hole a moment later.
	assert.True(t, TreeNodeStatusTransferLocked.ShouldMarkParentExited())
	assert.True(t, TreeNodeStatusRenewLocked.ShouldMarkParentExited())
}

func TestParentExitSweepExcludedStatuses(t *testing.T) {
	excluded := ParentExitSweepExcludedStatuses()
	for _, s := range excluded {
		assert.False(t, s.ShouldMarkParentExited(), "%s is excluded but marked as sweepable", s)
	}
	for _, v := range (TreeNodeStatus("")).Values() {
		if s := TreeNodeStatus(v); !s.ShouldMarkParentExited() {
			assert.Contains(t, excluded, s)
		}
	}
	// Confirmed chain state must never be downgraded to PARENT_EXITED.
	assert.Contains(t, excluded, TreeNodeStatusOnChain)
	assert.Contains(t, excluded, TreeNodeStatusExited)
	assert.Contains(t, excluded, TreeNodeStatusReimbursed)
	assert.NotContains(t, excluded, TreeNodeStatusAvailable)
}

func TestOccupancyTreeNodeStatuses_ExcludesTerminalAndAvailable(t *testing.T) {
	occupancy := OccupancyTreeNodeStatuses()
	assert.Len(t, occupancy, len((TreeNodeStatus("")).Values())-7)
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
	// CONSOLIDATED is non-terminal yet excluded: an exit-only node resting
	// until it is exited or aggregated further, not stuck work.
	assert.False(t, TreeNodeStatusConsolidated.CountsForOccupancy())
	assert.NotContains(t, occupancy, TreeNodeStatusConsolidated)
	// PARENT_EXITED counts: the watchtower still owes those children their
	// own broadcast work during unilateral exits.
	assert.Contains(t, occupancy, TreeNodeStatusParentExited)
	// LOST stays counted: it is the anomaly pool the growth alert watches.
	assert.Contains(t, occupancy, TreeNodeStatusLost)
	assert.Contains(t, occupancy, TreeNodeStatusOnChain)
	assert.NotContains(t, occupancy, TreeNodeStatusAggregated)
}
