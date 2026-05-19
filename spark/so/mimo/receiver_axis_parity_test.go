package mimo

import (
	"testing"
)

// TestCollapseTargetPrereqsMatchesCollapsingSet enforces parity between
// receiver_axis.collapsingReceiverStatuses (the set of r.status values that
// the SQL builders treat as collapsing classes) and
// sql_counter_swap.collapseTargetPrereqs (the per-class prereq sets that
// narrowingRedundantFor uses to decide when the t.status narrowing
// predicate can be safely dropped).
//
// If a new collapsing r.status is added to receiver_axis.go without a
// prereq entry in sql_counter_swap.go, this test fails — preventing the
// silent regression where narrowingRedundantFor would treat an unknown
// collapse class as "no prereqs to satisfy" and drop narrowing when it
// shouldn't. The narrowingRedundantFor code defends against that anyway
// (conservative return false on unknown target), but the test makes the
// intent explicit and the breakage loud.
func TestCollapseTargetPrereqsMatchesCollapsingSet(t *testing.T) {
	for target := range collapsingReceiverStatuses {
		if _, ok := collapseTargetPrereqs[target]; !ok {
			t.Errorf("collapsing r.status %q has no prereq entry in collapseTargetPrereqs — narrowingRedundantFor will conservatively preserve narrowing for it, but the prereqs should be declared explicitly", target)
		}
	}
	for target := range collapseTargetPrereqs {
		if _, ok := collapsingReceiverStatuses[target]; !ok {
			t.Errorf("collapseTargetPrereqs has entry for %q but it is not in collapsingReceiverStatuses — the prereqs are dead code", target)
		}
	}
}

// TestCollapseTargetPrereqsAreValidTransferStatuses guards against typos
// in the prereq lists by asserting every prereq is a recognized
// transfer.status enum value.
func TestCollapseTargetPrereqsAreValidTransferStatuses(t *testing.T) {
	for target, prereqs := range collapseTargetPrereqs {
		if len(prereqs) == 0 {
			t.Errorf("collapse target %q has empty prereq list — narrowingRedundantFor would treat it as always-redundant", target)
		}
		for _, p := range prereqs {
			_, inSender := senderToReceiverAxisMap[p]
			_, inGeneral := transferStatusToReceiverStatusMap[p]
			if !inSender && !inGeneral {
				t.Errorf("collapse target %q has prereq %q that is not in any t.status → r.status mapping", target, p)
			}
		}
	}
}
