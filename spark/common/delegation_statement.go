package common

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/lightsparkdev/spark/common/hashstructure"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// These statements define the canonical bytes an owner signs for each Spark Pull
// (delegated spending) lifecycle operation. They live in spark/common so the SDK
// can mirror the exact hashing, and every SO reconstructs the same statement from
// the request to verify the owner signature independently.

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// addSpenderTuple binds a single spender's authorization tuple: identity key,
// its three limit parameters, and the two unlimited flags. Status is
// intentionally excluded — it is server-derived, not owner-authorized. The
// unlimited flags waive enforcement ceilings, so they MUST be owner-signed:
// binding them here (statement v2) means a flag flipped in flight fails
// signature verification instead of silently widening authority.
func addSpenderTuple(h *hashstructure.Hasher, spender *pb.DelegationSpender) {
	h.AddBytes(spender.GetSpenderIdentityPublicKey()).
		AddUint64(spender.GetPerTxCapSats()).
		AddUint64(spender.GetRollingLimitSats()).
		AddUint64(spender.GetRollingWindowSeconds()).
		AddUint8(boolToUint8(spender.GetPerTxUnlimited())).
		AddUint8(boolToUint8(spender.GetRollingUnlimited()))
}

// CreateDelegationGrantStatement returns the bytes the owner signs to authorize a
// delegation grant. It binds every policy field: the grant id, owner, network,
// expiry, scopes, fees, version, and the full set of authorized spenders (each
// with its limits and unlimited flags). Spenders are sorted by identity key so
// the statement is independent of the order they appear on the wire.
//
// The domain tag is "v2": v2 added the per-spender unlimited flags to the
// spender tuple. There is no wire-level statement-version field — the tag IS
// the version — so v1 signatures simply fail verification (fail closed).
// Nothing signed under v1 was ever deployed.
func CreateDelegationGrantStatement(grant *pb.DelegationGrant) []byte {
	// Sorted so the statement does not depend on the order spenders arrive in.
	// The identity key alone is not a total order: slices.SortFunc is not stable,
	// so two spenders sharing a key but carrying different limits would hash
	// according to their wire order rather than their content. Duplicate keys are
	// rejected before a grant is applied; the tie-break keeps the statement a
	// function of the spender set even so, rather than of how it was serialized.
	spenders := slices.Clone(grant.GetSpenders())
	slices.SortFunc(spenders, func(a, b *pb.DelegationSpender) int {
		if c := bytes.Compare(a.GetSpenderIdentityPublicKey(), b.GetSpenderIdentityPublicKey()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.GetPerTxCapSats(), b.GetPerTxCapSats()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.GetRollingLimitSats(), b.GetRollingLimitSats()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.GetRollingWindowSeconds(), b.GetRollingWindowSeconds()); c != 0 {
			return c
		}
		if c := cmp.Compare(boolToUint8(a.GetPerTxUnlimited()), boolToUint8(b.GetPerTxUnlimited())); c != 0 {
			return c
		}
		return cmp.Compare(boolToUint8(a.GetRollingUnlimited()), boolToUint8(b.GetRollingUnlimited()))
	})

	h := hashstructure.NewHasher([]string{"spark", "delegation", "grant", "v2"}).
		AddString(grant.GetGrantId()).
		AddBytes(grant.GetOwnerIdentityPublicKey()).
		AddUint32(uint32(grant.GetNetwork())).
		AddUint64(uint64(grant.GetExpiryTime().GetSeconds())).
		AddUint32(uint32(grant.GetExpiryTime().GetNanos())).
		AddUint8(boolToUint8(grant.GetScopeTransfer())).
		AddUint8(boolToUint8(grant.GetScopeRenew())).
		AddUint8(boolToUint8(grant.GetScopeClaim())).
		AddUint64(grant.GetFeeFlatSats()).
		AddBytes(grant.GetFeeCollectorIdentityPublicKey()).
		AddUint64(grant.GetVersion()).
		AddUint64(uint64(len(spenders)))
	for _, spender := range spenders {
		addSpenderTuple(h, spender)
	}
	return h.Hash()
}

// CreateDelegationRevokeStatement returns the bytes the owner signs to revoke an
// entire grant. Binding the version prevents an unordered replay from resurrecting
// a stale revocation against a newer grant.
func CreateDelegationRevokeStatement(grantID string, version uint64, ownerIdentityPublicKey []byte) []byte {
	return hashstructure.NewHasher([]string{"spark", "delegation", "revoke", "v1"}).
		AddString(grantID).
		AddUint64(version).
		AddBytes(ownerIdentityPublicKey).
		Hash()
}

// CreateDelegationSpenderAddStatement returns the bytes the owner signs to
// authorize an additional spender on an existing grant. Tag "v2" matches the
// grant statement: the spender tuple now binds the unlimited flags.
func CreateDelegationSpenderAddStatement(grantID string, version uint64, spender *pb.DelegationSpender) []byte {
	h := hashstructure.NewHasher([]string{"spark", "delegation", "spender add", "v2"}).
		AddString(grantID).
		AddUint64(version)
	addSpenderTuple(h, spender)
	return h.Hash()
}

// CreateDelegationSpenderRevokeStatement returns the bytes the owner signs to
// remove a spender from a grant.
func CreateDelegationSpenderRevokeStatement(grantID string, version uint64, spenderIdentityPublicKey []byte) []byte {
	return hashstructure.NewHasher([]string{"spark", "delegation", "spender revoke", "v1"}).
		AddString(grantID).
		AddUint64(version).
		AddBytes(spenderIdentityPublicKey).
		Hash()
}

// CreateDecompositionInstallStatement returns the bytes the owner signs to install
// delegate-path decompositions on a set of the grant's leaves. It binds the grant
// id and the ordered (leaf id, delegate signing public key) pairs so the set of
// leaves and the key registered for each is fixed by the signature. Installs are
// sorted by leaf id for order-independence.
func CreateDecompositionInstallStatement(grantID string, installs []*pb.LeafDecompositionInstall) []byte {
	// Total order for the same reason as the grant statement: a leaf id repeated
	// with different delegate keys would otherwise hash by wire order.
	sorted := slices.Clone(installs)
	slices.SortFunc(sorted, func(a, b *pb.LeafDecompositionInstall) int {
		if c := cmp.Compare(a.GetLeafId(), b.GetLeafId()); c != 0 {
			return c
		}
		return bytes.Compare(a.GetDelegateSigningPublicKey(), b.GetDelegateSigningPublicKey())
	})

	h := hashstructure.NewHasher([]string{"spark", "delegation", "install", "v1"}).
		AddString(grantID).
		AddUint64(uint64(len(sorted)))
	for _, install := range sorted {
		h.AddString(install.GetLeafId()).
			AddBytes(install.GetDelegateSigningPublicKey())
	}
	return h.Hash()
}
