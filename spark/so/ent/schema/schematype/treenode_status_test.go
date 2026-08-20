package schematype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTreeNodeStatusIsTerminal(t *testing.T) {
	terminal := map[TreeNodeStatus]bool{
		TreeNodeStatusSplitted:                true,
		TreeNodeStatusAggregated:              true,
		TreeNodeStatusExited:                  true,
		TreeNodeStatusReimbursed:              true,
		TreeNodeStatusWatchtowerExited:        true,
		TreeNodeStatusWatchtowerExitRecovered: true,
		TreeNodeStatusCreationAbandoned:       true,
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

func TestTreeNodeStatusShouldMarkWatchtowerExited(t *testing.T) {
	// The only difference from the PARENT_EXITED policy, and the reason the
	// watchtower pass runs after the raw cascade rather than before it.
	assert.True(t, TreeNodeStatusParentExited.ShouldMarkWatchtowerExited())
	assert.False(t, TreeNodeStatusParentExited.ShouldMarkParentExited())
	for _, v := range (TreeNodeStatus("")).Values() {
		s := TreeNodeStatus(v)
		if s == TreeNodeStatusParentExited {
			continue
		}
		assert.Equal(t, s.ShouldMarkParentExited(), s.ShouldMarkWatchtowerExited(),
			"ShouldMarkWatchtowerExited(%s) must match the PARENT_EXITED policy", s)
	}
	// Being terminal makes this status safe for the watchtower to ignore, not
	// safe to write over confirmed chain state or an aggregation marker.
	assert.False(t, TreeNodeStatusOnChain.ShouldMarkWatchtowerExited())
	assert.False(t, TreeNodeStatusExited.ShouldMarkWatchtowerExited())
	assert.False(t, TreeNodeStatusSplitted.ShouldMarkWatchtowerExited())
	assert.False(t, TreeNodeStatusConsolidated.ShouldMarkWatchtowerExited())
	// Re-confirmation in a later block must not churn update_time.
	assert.False(t, TreeNodeStatusWatchtowerExited.ShouldMarkWatchtowerExited())
	// A later ancestor confirmation must not walk back a recovery the SE has
	// already co-signed.
	assert.False(t, TreeNodeStatusWatchtowerExitRecovered.ShouldMarkWatchtowerExited())
}

func TestWatchtowerExitSweepExcludedStatuses(t *testing.T) {
	excluded := WatchtowerExitSweepExcludedStatuses()
	for _, s := range excluded {
		assert.False(t, s.ShouldMarkWatchtowerExited(), "%s is excluded but marked as sweepable", s)
	}
	for _, v := range (TreeNodeStatus("")).Values() {
		if s := TreeNodeStatus(v); !s.ShouldMarkWatchtowerExited() {
			assert.Contains(t, excluded, s)
		}
	}
	// The upgrade this pass exists to perform.
	assert.NotContains(t, excluded, TreeNodeStatusParentExited)
	assert.Contains(t, ParentExitSweepExcludedStatuses(), TreeNodeStatusParentExited)
}

func TestTreeNodeStatusWatchtowerExitedIsExitedToL1(t *testing.T) {
	// setAvailableUnlessExitedToL1 reads this to decide whether a completing
	// transfer may return a leaf to AVAILABLE. False here would let the next
	// finalize revive a leaf whose exit path is gone.
	assert.True(t, TreeNodeStatusWatchtowerExited.IsExitedToL1())
	assert.False(t, TreeNodeStatusWatchtowerExited.CanBecomeAvailable())
}

func TestTreeNodeStatusWatchtowerExitRecovered(t *testing.T) {
	// The whole point of the status: the SE never observes the broadcast, so the
	// signature — not a confirmation — is what keeps the leaf out of the
	// transferable pool.
	assert.True(t, TreeNodeStatusWatchtowerExitRecovered.IsTerminal())
	assert.True(t, TreeNodeStatusWatchtowerExitRecovered.IsExitedToL1())
	assert.False(t, TreeNodeStatusWatchtowerExitRecovered.CanBecomeAvailable())
	// Terminal, so the watchtower has no path left to work and the node drops
	// out of the occupancy metrics rather than reading as stuck.
	assert.False(t, TreeNodeStatusWatchtowerExitRecovered.CountsForOccupancy())
	// Neither exit sweep may overwrite it: both write a weaker claim.
	assert.Contains(t, ParentExitSweepExcludedStatuses(), TreeNodeStatusWatchtowerExitRecovered)
	assert.Contains(t, WatchtowerExitSweepExcludedStatuses(), TreeNodeStatusWatchtowerExitRecovered)
}

func TestTreeNodeStatusCreationAbandoned(t *testing.T) {
	// The funding tx never confirmed and the creation flow is dead, so nothing
	// in the tree can ever reach the chain: never revivable, invisible to
	// occupancy, no watchtower work, and never overwritten by an exit sweep.
	assert.True(t, TreeNodeStatusCreationAbandoned.IsTerminal())
	assert.False(t, TreeNodeStatusCreationAbandoned.CanBecomeAvailable())
	assert.False(t, TreeNodeStatusCreationAbandoned.CountsForOccupancy())
	assert.False(t, TreeNodeStatusCreationAbandoned.IsExitedToL1())
	assert.Contains(t, ParentExitSweepExcludedStatuses(), TreeNodeStatusCreationAbandoned)
	assert.Contains(t, WatchtowerExitSweepExcludedStatuses(), TreeNodeStatusCreationAbandoned)
}

func TestOccupancyTreeNodeStatuses_ExcludesTerminalAndAvailable(t *testing.T) {
	occupancy := OccupancyTreeNodeStatuses()
	assert.Len(t, occupancy, len((TreeNodeStatus("")).Values())-10)
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
