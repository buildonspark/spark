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
	// TreeNodeStatusSplitted is the status of a tree node that is splitted.
	TreeNodeStatusSplitted TreeNodeStatus = "SPLITTED"
	// TreeNodeStatusAggregated is the status of a tree node that is aggregated, this is a terminal state.
	TreeNodeStatusAggregated TreeNodeStatus = "AGGREGATED"
	// TreeNodeStatusOnChain is the status of a tree node that is on chain, this is a terminal state.
	TreeNodeStatusOnChain TreeNodeStatus = "ON_CHAIN"
	// TreeNodeStatusExited is the status of a tree node where the whole tree exited, this is a terminal state.
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
