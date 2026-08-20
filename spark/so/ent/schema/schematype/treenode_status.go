package schematype

// TreeNodeStatus is the status of a tree node.
type TreeNodeStatus string

const (
	// TreeNodeStatusCreating is the status of a tree node that is under creation.
	TreeNodeStatusCreating TreeNodeStatus = "CREATING"
	// TreeNodeStatusAvailable is the status of a tree node that is available.
	TreeNodeStatusAvailable TreeNodeStatus = "AVAILABLE"
	// TreeNodeStatusFrozenByIssuer is the status of a tree node that is frozen by the issuer.
	TreeNodeStatusFrozenByIssuer TreeNodeStatus = "FROZEN_BY_ISSUER"
	// TreeNodeStatusTransferLocked is the status of a tree node that is transfer locked.
	TreeNodeStatusTransferLocked TreeNodeStatus = "TRANSFER_LOCKED"
	// TreeNodeStatusSplitLocked is the status of a tree node that is split locked.
	TreeNodeStatusSplitLocked TreeNodeStatus = "SPLIT_LOCKED"
	// TreeNodeStatusSplitted is the status of a tree node that is splitted. Terminal for transfers.
	TreeNodeStatusSplitted TreeNodeStatus = "SPLITTED"
	// TreeNodeStatusAggregated is the status of a tree node that is aggregated. Terminal for transfers.
	TreeNodeStatusAggregated TreeNodeStatus = "AGGREGATED"
	// TreeNodeStatusOnChain means the node tx is confirmed. Watchtower still needs to watch refund tx.
	TreeNodeStatusOnChain TreeNodeStatus = "ON_CHAIN"
	// TreeNodeStatusExited means the refund tx is confirmed. Fully terminal.
	TreeNodeStatusExited TreeNodeStatus = "EXITED"
	// TreeNodeStatusAggregateLock is the status of a tree node that is aggregate locked.
	TreeNodeStatusAggregateLock TreeNodeStatus = "AGGREGATE_LOCK"
	// TreeNodeStatusInvestigation is the status of a tree node that is investigated.
	TreeNodeStatusInvestigation TreeNodeStatus = "INVESTIGATION"
	// TreeNodeStatusLost is the status of a tree node that is in a unrecoverable bad state.
	TreeNodeStatusLost TreeNodeStatus = "LOST"
	// TreeNodeStatusReimbursed is the status of a tree node that is reimbursed after LOST.
	TreeNodeStatusReimbursed TreeNodeStatus = "REIMBURSED"
	// This node is not valid for transfer, timelock refresh, etc., because the parent node is in the exiting process.
	TreeNodeStatusParentExited TreeNodeStatus = "PARENT_EXITED"
	// TreeNodeStatusRenewLocked is the status of a tree node that is locked for renewal.
	TreeNodeStatusRenewLocked TreeNodeStatus = "RENEW_LOCKED"
	// TreeNodeStatusConsolidated is the status of a node whose subtree was
	// aggregated back into it (AggregateLeaves): it carries live exit
	// transactions signed under the aggregated leaf key and is exit-only —
	// not transferable, renewable, or splittable. It may be aggregated
	// further up the tree.
	TreeNodeStatusConsolidated TreeNodeStatus = "CONSOLIDATED"
	// TreeNodeStatusWatchtowerExited is the status of a node below one whose direct
	// tx confirmed, which conflict-spends the outpoint its own raw tx names: the
	// exit path is gone rather than pending, and recovery moves to L1. Terminal.
	TreeNodeStatusWatchtowerExited TreeNodeStatus = "WATCHTOWER_EXITED"
	// TreeNodeStatusWatchtowerExitRecovered is the status of a WATCHTOWER_EXITED node
	// whose owner has since co-signed a transaction spending the swept output with
	// the SE. Written when the signature is issued rather than when it confirms:
	// holding a valid spend is already enough to double-claim the value against an
	// off-chain transfer, and the SE never sees the broadcast. Terminal.
	//
	// Anything gating a co-signed spend of the swept output must name this status
	// and WATCHTOWER_EXITED explicitly, never IsExitedToL1 — three of that
	// predicate's five statuses have no such output to spend.
	TreeNodeStatusWatchtowerExitRecovered TreeNodeStatus = "WATCHTOWER_EXIT_RECOVERED"
	// TreeNodeStatusCreationAbandoned is the status of a node in a tree whose
	// creation was abandoned: the tree never left PENDING, no node was ever
	// signed, and the funding transaction was never confirmed — so no
	// transaction in the tree can ever reach the chain. Written by the
	// retire_abandoned_pending_trees task. Terminal.
	TreeNodeStatusCreationAbandoned TreeNodeStatus = "CREATION_ABANDONED"
)

// CanBecomeAvailable reports whether a tree node currently in this status is
// allowed to transition to AVAILABLE. Terminal states whose backing UTXO has
// been consumed on-chain (or has otherwise been retired) must never be revived
// off-chain — see SP-3049 for the cancel-transfer revival exploit this guards
// against.
func (s TreeNodeStatus) CanBecomeAvailable() bool {
	switch s {
	case TreeNodeStatusSplitted,
		TreeNodeStatusOnChain,
		TreeNodeStatusExited,
		TreeNodeStatusParentExited,
		TreeNodeStatusReimbursed,
		TreeNodeStatusConsolidated,
		TreeNodeStatusWatchtowerExited,
		TreeNodeStatusWatchtowerExitRecovered,
		TreeNodeStatusCreationAbandoned:
		return false
	case TreeNodeStatusCreating,
		TreeNodeStatusAvailable,
		TreeNodeStatusFrozenByIssuer,
		TreeNodeStatusTransferLocked,
		TreeNodeStatusSplitLocked,
		TreeNodeStatusAggregated,
		TreeNodeStatusAggregateLock,
		TreeNodeStatusInvestigation,
		TreeNodeStatusLost,
		TreeNodeStatusRenewLocked:
		return true
	}
	// Fail-safe: a TreeNodeStatus that didn't match either arm above is treated
	// as non-revivable. The `exhaustive` linter makes this branch unreachable
	// today, but defaulting to false ensures any newly-added status is opted
	// into AVAILABLE transitions explicitly rather than by accident.
	return false
}

// IsExitedToL1 reports whether the node has exited (or is exiting) to Bitcoin
// L1: its own node tx confirmed (ON_CHAIN), its refund tx confirmed (EXITED),
// or an ancestor's exit confirmed (PARENT_EXITED, WATCHTOWER_EXITED), or its
// owner holds a co-signed spend of the swept output (WATCHTOWER_EXIT_RECOVERED).
// These are the only non-claimable-by-default statuses a receiver may still
// claim through (the claim preserves them). Distinct from CanBecomeAvailable
// above, whose false set additionally includes SPLITTED and REIMBURSED —
// statuses that must remain neither claimable nor revivable.
//
// The last two are here for the preservation half: setAvailableUnlessExitedToL1
// reads this to decide whether a completing transfer may return a leaf to
// AVAILABLE, so excluding them would revive a leaf whose exit path is gone or
// whose value has already been claimed on L1.
//
// Claimability is all this answers, not recoverability: only WATCHTOWER_EXITED
// and WATCHTOWER_EXIT_RECOVERED leave an output under the leaf key to spend.
func (s TreeNodeStatus) IsExitedToL1() bool {
	return s == TreeNodeStatusOnChain ||
		s == TreeNodeStatusExited ||
		s == TreeNodeStatusParentExited ||
		s == TreeNodeStatusWatchtowerExited ||
		s == TreeNodeStatusWatchtowerExitRecovered
}

// ShouldMarkParentExited reports whether a child in this status should be swept
// to PARENT_EXITED when one of its parent's transactions confirms
// (tree.MarkExitingNodes).
//
// PARENT_EXITED exists to mark a node unusable for transfer or renewal while an
// ancestor exits to L1, so it is only worth writing over a status that could
// otherwise still reach AVAILABLE. Anywhere else it destroys information the
// exit itself depends on. The default is to sweep: a status added without
// classification blocks transfers rather than silently leaving a node
// spendable under a dead parent.
//
// aggregateLeavesPriorLeafStatus (so/handler/aggregate_leaves_flow_handler.go)
// derives the same judgment by hand for a single leaf and is not wired to this
// predicate; keep the two in step.
func (s TreeNodeStatus) ShouldMarkParentExited() bool {
	switch s {
	case TreeNodeStatusSplitted:
		// A branch, whose children spend its output — a parent confirming
		// advances this node's exit rather than invalidating it. SPLITTED is
		// also terminal to the watchtower while PARENT_EXITED is not, so
		// sweeping a branch hands back a tx whose sequence has the relative
		// timelock disabled; checkAndBroadcastNodeTx rejects it on every scan
		// tick forever.
		return false
	case TreeNodeStatusOnChain, TreeNodeStatusExited, TreeNodeStatusParentExited, TreeNodeStatusWatchtowerExited, TreeNodeStatusWatchtowerExitRecovered:
		// Already at least as strong a claim as PARENT_EXITED. Overwriting
		// would downgrade confirmed chain state — the sweep otherwise
		// clobbers a branch marked ON_CHAIN earlier in the same call — or, for
		// an already-swept node, churn update_time on every later block that
		// confirms another of the parent's transactions. WATCHTOWER_EXIT_RECOVERED
		// additionally records a spend the SE co-signed, which no chain
		// observation supersedes.
		return false

	// Leaf-aggregation statuses are already unusable for transfer and
	// renewal, so overwriting one only destroys the marker it carries:
	case TreeNodeStatusConsolidated:
		// Overwriting it destroys the one marker the watchtower uses to find
		// that node's exit package, and a confirming parent is precisely when
		// that package becomes broadcastable, so the owner would lose
		// protection at the moment they need it.
		return false
	case TreeNodeStatusAggregated:
		// Retired nodes keep their transaction bytes, and AGGREGATED is what
		// keeps the watchtower from broadcasting them. PARENT_EXITED is not
		// terminal, so the sweep would hand those superseded transactions back
		// to the watchtower to retry forever against an outpoint the
		// consolidated package already spent.
		return false
	case TreeNodeStatusAggregateLock:
		// A leaf mid-aggregation, whose stored package is about to be
		// replaced. AGGREGATE_LOCK is the only thing stopping the watchtower
		// from broadcasting that soon-to-be-stale package, so replacing it
		// with PARENT_EXITED would publish an old state right as the exit
		// package is being installed — irreversible, unlike the cost of not
		// sweeping (a rolled-back leaf briefly sits AVAILABLE under an exited
		// parent until the next pass).
		return false

	case TreeNodeStatusCreationAbandoned:
		// The funding transaction never reached the chain, so no ancestor exit
		// can affect this node; the sweep would only destroy the abandonment
		// marker and re-admit the node to the watchtower's work set.
		return false

	case TreeNodeStatusReimbursed:
		// Already settled: the operator has paid this node out. PARENT_EXITED is
		// the weaker claim and the write is one-way — nothing un-sets it, and
		// there is no reorg-rollback path — so sweeping would lose the marker
		// for good. It would also re-admit the node to work it is finished
		// with: PARENT_EXITED satisfies IsExitedToL1(), which is what
		// validateFinalizeTransferLeafCanComplete gates on, and REIMBURSED is
		// in the watchtower's terminal set while PARENT_EXITED is not.
		return false

	case TreeNodeStatusCreating,
		TreeNodeStatusAvailable,
		TreeNodeStatusFrozenByIssuer,
		TreeNodeStatusTransferLocked,
		TreeNodeStatusSplitLocked,
		TreeNodeStatusRenewLocked,
		TreeNodeStatusInvestigation,
		TreeNodeStatusLost:
		// SPLIT_LOCKED covers renew-created split nodes: sweeping them is what
		// keeps them non-terminal and inside the watchtower's work set, which
		// is the defensive sweep that parks funds beyond a previous holder's
		// reach. TRANSFER_LOCKED and RENEW_LOCKED are transient and drain back
		// to AVAILABLE, so skipping them would reopen the hole a moment later.
		return true
	}
	// See the doc comment: an unclassified status blocks rather than allows.
	return true
}

// ParentExitSweepExcludedStatuses returns the statuses MarkExitingNodes must
// not overwrite with PARENT_EXITED, derived from ShouldMarkParentExited so the
// query predicate cannot drift from the policy.
func ParentExitSweepExcludedStatuses() []TreeNodeStatus {
	var out []TreeNodeStatus
	for _, v := range (TreeNodeStatus("")).Values() {
		if s := TreeNodeStatus(v); !s.ShouldMarkParentExited() {
			out = append(out, s)
		}
	}
	return out
}

// ShouldMarkWatchtowerExited reports whether a descendant in this status should be
// marked WATCHTOWER_EXITED when an ancestor's direct tx confirms. The
// ShouldMarkParentExited policy plus one upgrade: a confirmed direct tx is the
// event PARENT_EXITED was anticipating, so replacing it loses nothing.
func (s TreeNodeStatus) ShouldMarkWatchtowerExited() bool {
	return s == TreeNodeStatusParentExited || s.ShouldMarkParentExited()
}

// WatchtowerExitSweepExcludedStatuses returns the statuses MarkExitingNodes must
// not overwrite with WATCHTOWER_EXITED, derived from ShouldMarkWatchtowerExited
// so the query predicate cannot drift from the policy.
func WatchtowerExitSweepExcludedStatuses() []TreeNodeStatus {
	var out []TreeNodeStatus
	for _, v := range (TreeNodeStatus("")).Values() {
		if s := TreeNodeStatus(v); !s.ShouldMarkWatchtowerExited() {
			out = append(out, s)
		}
	}
	return out
}

// Values returns the values of the tree node status.
func (TreeNodeStatus) Values() []string {
	return []string{
		string(TreeNodeStatusCreating),
		string(TreeNodeStatusAvailable),
		string(TreeNodeStatusFrozenByIssuer),
		string(TreeNodeStatusTransferLocked),
		string(TreeNodeStatusSplitLocked),
		string(TreeNodeStatusSplitted),
		string(TreeNodeStatusAggregated),
		string(TreeNodeStatusOnChain),
		string(TreeNodeStatusAggregateLock),
		string(TreeNodeStatusExited),
		string(TreeNodeStatusInvestigation),
		string(TreeNodeStatusLost),
		string(TreeNodeStatusReimbursed),
		string(TreeNodeStatusParentExited),
		string(TreeNodeStatusRenewLocked),
		string(TreeNodeStatusConsolidated),
		string(TreeNodeStatusWatchtowerExited),
		string(TreeNodeStatusWatchtowerExitRecovered),
		string(TreeNodeStatusCreationAbandoned),
	}
}

// IsTerminal reports whether a tree node in this status has left the live
// pool for good. Deliberate divergences from CanBecomeAvailable():
// AGGREGATED is terminal here (consumed history in practice; its
// revivability is an SP-3049-guard edge case), while ON_CHAIN and
// PARENT_EXITED are non-terminal — the watchtower still owes both
// broadcast/refund work (QueryBroadcastableNodes excludes neither) and
// they drain to EXITED.
func (s TreeNodeStatus) IsTerminal() bool {
	switch s {
	case TreeNodeStatusSplitted,
		TreeNodeStatusAggregated,
		TreeNodeStatusExited,
		TreeNodeStatusReimbursed,
		TreeNodeStatusWatchtowerExited,
		TreeNodeStatusWatchtowerExitRecovered,
		TreeNodeStatusCreationAbandoned:
		return true
	case TreeNodeStatusCreating,
		TreeNodeStatusAvailable,
		TreeNodeStatusFrozenByIssuer,
		TreeNodeStatusTransferLocked,
		TreeNodeStatusSplitLocked,
		TreeNodeStatusOnChain,
		TreeNodeStatusAggregateLock,
		TreeNodeStatusInvestigation,
		TreeNodeStatusLost,
		TreeNodeStatusParentExited,
		TreeNodeStatusRenewLocked,
		TreeNodeStatusConsolidated:
		return false
	}
	// A status added without classification defaults to non-terminal so it
	// appears in occupancy counts rather than silently vanishing.
	return false
}

// CountsForOccupancy reports whether rows in this status belong in the
// occupancy metrics. Terminal statuses are consumed history;
// AVAILABLE is excluded even though it is non-terminal because available
// leaves are the resting spendable pool, not in-flight work — the stuck-fund
// signal the metrics exist for. SPLIT_LOCKED is excluded for the same
// reason: it is the permanent resting status of renew-created split nodes
// (no transition leaves it), so it grows with every leaf renewal by design.
// CONSOLIDATED is likewise a healthy resting state (an exit-only node waiting
// to be exited or aggregated further), not stuck work.
func (s TreeNodeStatus) CountsForOccupancy() bool {
	return !s.IsTerminal() &&
		s != TreeNodeStatusAvailable &&
		s != TreeNodeStatusSplitLocked &&
		s != TreeNodeStatusConsolidated
}

// OccupancyTreeNodeStatuses returns the statuses counted by the
// occupancy metrics.
func OccupancyTreeNodeStatuses() []TreeNodeStatus {
	var out []TreeNodeStatus
	for _, v := range (TreeNodeStatus("")).Values() {
		if s := TreeNodeStatus(v); s.CountsForOccupancy() {
			out = append(out, s)
		}
	}
	return out
}
