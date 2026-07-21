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
		TreeNodeStatusReimbursed:
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
// or an ancestor's exit confirmed (PARENT_EXITED). These are the only
// non-claimable-by-default statuses a receiver may still claim through (the
// claim preserves them). Distinct from CanBecomeAvailable above, whose false
// set additionally includes SPLITTED and REIMBURSED — statuses that must
// remain neither claimable nor revivable.
func (s TreeNodeStatus) IsExitedToL1() bool {
	return s == TreeNodeStatusOnChain || s == TreeNodeStatusExited || s == TreeNodeStatusParentExited
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
		TreeNodeStatusReimbursed:
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
		TreeNodeStatusRenewLocked:
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
func (s TreeNodeStatus) CountsForOccupancy() bool {
	return !s.IsTerminal() &&
		s != TreeNodeStatusAvailable &&
		s != TreeNodeStatusSplitLocked
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
