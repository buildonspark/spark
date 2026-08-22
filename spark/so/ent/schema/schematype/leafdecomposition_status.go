package schematype

// LeafDecompositionStatus is the lifecycle status of an installed delegate-path
// decomposition on a single leaf.
type LeafDecompositionStatus string

const (
	// LeafDecompositionStatusActive is an installed, usable delegate-path
	// decomposition. Only this status may contribute a delegate-path signature.
	LeafDecompositionStatusActive LeafDecompositionStatus = "ACTIVE"
	// LeafDecompositionStatusConsumed marks a decomposition whose leaf has been
	// spent or otherwise transitioned, so the SE2 keyshare was hard-deleted.
	LeafDecompositionStatusConsumed LeafDecompositionStatus = "CONSUMED"
	// LeafDecompositionStatusRevoked marks a decomposition killed by revocation
	// (the SE2 keyshare was hard-deleted).
	LeafDecompositionStatusRevoked LeafDecompositionStatus = "REVOKED"
	// LeafDecompositionStatusRevokePending marks a decomposition whose policy
	// revocation has landed but whose SE2 share deletion has not yet completed
	// across the federation.
	LeafDecompositionStatusRevokePending LeafDecompositionStatus = "REVOKE_PENDING"
	// LeafDecompositionStatusExpired marks a decomposition retired because the
	// grant authorizing it expired. Expiry is the ordinary way a delegation ends,
	// so it must free the leaf's single ACTIVE slot and hard-delete the SE2 share
	// exactly as revocation does; leaving the row ACTIVE would hold the slot
	// against a dead grant and keep the delegate path cryptographically alive.
	LeafDecompositionStatusExpired LeafDecompositionStatus = "EXPIRED"
)

// CanSign reports whether a decomposition in this status may contribute to
// delegate-path FROST signing. Only ACTIVE may sign; CONSUMED, REVOKED, and
// REVOKE_PENDING must never produce a delegate-path signature.
func (s LeafDecompositionStatus) CanSign() bool {
	return s == LeafDecompositionStatusActive
}

// Values returns the values of the leaf decomposition status.
func (LeafDecompositionStatus) Values() []string {
	return []string{
		string(LeafDecompositionStatusActive),
		string(LeafDecompositionStatusConsumed),
		string(LeafDecompositionStatusRevoked),
		string(LeafDecompositionStatusRevokePending),
		string(LeafDecompositionStatusExpired),
	}
}
