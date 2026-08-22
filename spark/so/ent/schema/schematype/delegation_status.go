package schematype

// DelegationStatus is the lifecycle status of a delegation grant or one of its
// authorized spender records. Records are tombstoned (REVOKED), never deleted,
// so unordered gossip cannot resurrect a stale ACTIVE state.
type DelegationStatus string

const (
	// DelegationStatusActive is the status of an in-force grant or spender.
	DelegationStatusActive DelegationStatus = "ACTIVE"
	// DelegationStatusRevoked is the tombstone status after an owner-signed revoke.
	DelegationStatusRevoked DelegationStatus = "REVOKED"
)

// Values returns the values of the delegation status.
func (DelegationStatus) Values() []string {
	return []string{
		string(DelegationStatusActive),
		string(DelegationStatusRevoked),
	}
}
