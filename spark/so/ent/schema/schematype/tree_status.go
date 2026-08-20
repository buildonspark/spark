package schematype

// TreeStatus is the status of a tree node.
type TreeStatus string

const (
	// TreeStatusPending is the status of a tree that the base L1 transaction is not confirmed yet.
	TreeStatusPending TreeStatus = "PENDING"
	// TreeStatusAvailable is the status of a tree that the base L1 transaction is confirmed.
	TreeStatusAvailable TreeStatus = "AVAILABLE"
	// TreeStatusExited is the status of a tree that has exited.
	TreeStatusExited TreeStatus = "EXITED"
	// TreeStatusCreationAbandoned is the status of a pending tree whose funding
	// transaction was never confirmed and whose creation flow was abandoned, so
	// the tree can never become available. Written by the
	// retire_abandoned_pending_trees task. Terminal.
	TreeStatusCreationAbandoned TreeStatus = "CREATION_ABANDONED"
)

// Values returns the values of the tree node status.
func (TreeStatus) Values() []string {
	return []string{
		string(TreeStatusPending),
		string(TreeStatusAvailable),
		string(TreeStatusExited),
		string(TreeStatusCreationAbandoned),
	}
}
