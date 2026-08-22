package common

import (
	"testing"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func pubkey(b byte) []byte {
	pk := make([]byte, 33)
	pk[0] = 0x02
	pk[32] = b
	return pk
}

func baseGrant() *pb.DelegationGrant {
	return &pb.DelegationGrant{
		GrantId:                       "11111111-1111-1111-1111-111111111111",
		OwnerIdentityPublicKey:        pubkey(0x01),
		Network:                       pb.Network_REGTEST,
		ExpiryTime:                    &timestamppb.Timestamp{Seconds: 1_800_000_000, Nanos: 7},
		ScopeTransfer:                 true,
		ScopeRenew:                    false,
		ScopeClaim:                    true,
		FeeFlatSats:                   10,
		FeeCollectorIdentityPublicKey: pubkey(0x09),
		Version:                       3,
		Spenders: []*pb.DelegationSpender{
			{SpenderIdentityPublicKey: pubkey(0xaa), PerTxCapSats: 100, RollingLimitSats: 500, RollingWindowSeconds: 86400},
			{SpenderIdentityPublicKey: pubkey(0xbb), PerTxCapSats: 200, RollingLimitSats: 900, RollingWindowSeconds: 3600},
		},
	}
}

func TestDelegationGrantStatementBindsAllFields(t *testing.T) {
	base := CreateDelegationGrantStatement(baseGrant())

	tests := []struct {
		name   string
		mutate func(*pb.DelegationGrant)
	}{
		{"grant_id", func(g *pb.DelegationGrant) { g.GrantId = "22222222-2222-2222-2222-222222222222" }},
		{"owner", func(g *pb.DelegationGrant) { g.OwnerIdentityPublicKey = pubkey(0x02) }},
		{"network", func(g *pb.DelegationGrant) { g.Network = pb.Network_MAINNET }},
		{"expiry_seconds", func(g *pb.DelegationGrant) { g.ExpiryTime = &timestamppb.Timestamp{Seconds: 1_800_000_001, Nanos: 7} }},
		{"expiry_nanos", func(g *pb.DelegationGrant) { g.ExpiryTime = &timestamppb.Timestamp{Seconds: 1_800_000_000, Nanos: 8} }},
		{"scope_transfer", func(g *pb.DelegationGrant) { g.ScopeTransfer = false }},
		{"scope_renew", func(g *pb.DelegationGrant) { g.ScopeRenew = true }},
		{"scope_claim", func(g *pb.DelegationGrant) { g.ScopeClaim = false }},
		{"fee_flat_sats", func(g *pb.DelegationGrant) { g.FeeFlatSats = 11 }},
		{"fee_collector", func(g *pb.DelegationGrant) { g.FeeCollectorIdentityPublicKey = pubkey(0x0a) }},
		{"version", func(g *pb.DelegationGrant) { g.Version = 4 }},
		{"spender_pubkey", func(g *pb.DelegationGrant) { g.Spenders[0].SpenderIdentityPublicKey = pubkey(0xac) }},
		{"spender_per_tx_cap", func(g *pb.DelegationGrant) { g.Spenders[0].PerTxCapSats = 101 }},
		{"spender_rolling_limit", func(g *pb.DelegationGrant) { g.Spenders[0].RollingLimitSats = 501 }},
		{"spender_rolling_window", func(g *pb.DelegationGrant) { g.Spenders[0].RollingWindowSeconds = 86401 }},
		{"spender_per_tx_unlimited", func(g *pb.DelegationGrant) { g.Spenders[0].PerTxUnlimited = true }},
		{"spender_rolling_unlimited", func(g *pb.DelegationGrant) { g.Spenders[0].RollingUnlimited = true }},
		{"spender_added", func(g *pb.DelegationGrant) {
			g.Spenders = append(g.Spenders, &pb.DelegationSpender{SpenderIdentityPublicKey: pubkey(0xcc), PerTxCapSats: 1})
		}},
		{"spender_removed", func(g *pb.DelegationGrant) { g.Spenders = g.GetSpenders()[:1] }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := baseGrant()
			tt.mutate(g)
			require.NotEqual(t, base, CreateDelegationGrantStatement(g), "flipping %s must change the grant statement", tt.name)
		})
	}
}

func TestDelegationGrantStatementSpenderOrderCanonical(t *testing.T) {
	g := baseGrant()

	reordered := baseGrant()
	reordered.Spenders = []*pb.DelegationSpender{reordered.GetSpenders()[1], reordered.GetSpenders()[0]}

	require.Equal(t,
		CreateDelegationGrantStatement(g),
		CreateDelegationGrantStatement(reordered),
		"spender order must not affect the grant statement",
	)
}

func TestDelegationGrantStatementExcludesOutputOnlyFields(t *testing.T) {
	g := baseGrant()
	base := CreateDelegationGrantStatement(g)

	// owner_signature and status (grant + spender) are server/output fields and
	// must not be part of what the owner signs.
	g.OwnerSignature = []byte{0xde, 0xad}
	g.Status = pb.DelegationStatus_DELEGATION_STATUS_REVOKED
	g.Spenders[0].Status = pb.DelegationStatus_DELEGATION_STATUS_REVOKED
	require.Equal(t, base, CreateDelegationGrantStatement(g))
}

func TestRevokeStatementBindsGrantIDAndVersion(t *testing.T) {
	owner := pubkey(0x01)
	base := CreateDelegationRevokeStatement("11111111-1111-1111-1111-111111111111", 3, owner)

	require.NotEqual(t, base, CreateDelegationRevokeStatement("22222222-2222-2222-2222-222222222222", 3, owner))
	require.NotEqual(t, base, CreateDelegationRevokeStatement("11111111-1111-1111-1111-111111111111", 4, owner))
	require.NotEqual(t, base, CreateDelegationRevokeStatement("11111111-1111-1111-1111-111111111111", 3, pubkey(0x02)))
	require.Equal(t, base, CreateDelegationRevokeStatement("11111111-1111-1111-1111-111111111111", 3, owner))
}

func TestDelegationSpenderStatementsBindFields(t *testing.T) {
	grantID := "11111111-1111-1111-1111-111111111111"
	spender := &pb.DelegationSpender{
		SpenderIdentityPublicKey: pubkey(0xaa),
		PerTxCapSats:             100,
		RollingLimitSats:         500,
		RollingWindowSeconds:     86400,
	}

	addBase := CreateDelegationSpenderAddStatement(grantID, 5, spender)

	addTests := []struct {
		name   string
		mutate func(*pb.DelegationSpender)
	}{
		{"pubkey", func(s *pb.DelegationSpender) { s.SpenderIdentityPublicKey = pubkey(0xab) }},
		{"per_tx_cap", func(s *pb.DelegationSpender) { s.PerTxCapSats = 101 }},
		{"rolling_limit", func(s *pb.DelegationSpender) { s.RollingLimitSats = 501 }},
		{"rolling_window", func(s *pb.DelegationSpender) { s.RollingWindowSeconds = 86401 }},
		{"per_tx_unlimited", func(s *pb.DelegationSpender) { s.PerTxUnlimited = true }},
		{"rolling_unlimited", func(s *pb.DelegationSpender) { s.RollingUnlimited = true }},
	}
	for _, tt := range addTests {
		t.Run("add_"+tt.name, func(t *testing.T) {
			s := &pb.DelegationSpender{
				SpenderIdentityPublicKey: spender.GetSpenderIdentityPublicKey(),
				PerTxCapSats:             spender.GetPerTxCapSats(),
				RollingLimitSats:         spender.GetRollingLimitSats(),
				RollingWindowSeconds:     spender.GetRollingWindowSeconds(),
			}
			tt.mutate(s)
			require.NotEqual(t, addBase, CreateDelegationSpenderAddStatement(grantID, 5, s))
		})
	}
	// Grant id and version are also bound.
	require.NotEqual(t, addBase, CreateDelegationSpenderAddStatement("22222222-2222-2222-2222-222222222222", 5, spender))
	require.NotEqual(t, addBase, CreateDelegationSpenderAddStatement(grantID, 6, spender))

	// Add and revoke statements are domain-separated even for identical inputs.
	revokeBase := CreateDelegationSpenderRevokeStatement(grantID, 5, spender.GetSpenderIdentityPublicKey())
	require.NotEqual(t, addBase, revokeBase)

	require.NotEqual(t, revokeBase, CreateDelegationSpenderRevokeStatement(grantID, 5, pubkey(0xab)))
	require.NotEqual(t, revokeBase, CreateDelegationSpenderRevokeStatement(grantID, 6, spender.GetSpenderIdentityPublicKey()))
	require.NotEqual(t, revokeBase, CreateDelegationSpenderRevokeStatement("22222222-2222-2222-2222-222222222222", 5, spender.GetSpenderIdentityPublicKey()))
	require.Equal(t, revokeBase, CreateDelegationSpenderRevokeStatement(grantID, 5, spender.GetSpenderIdentityPublicKey()))
}

// The statement must be a function of the spender set, not of the order the
// spenders were serialized in. Sorting on the identity key alone is not a total
// order, so duplicate keys carrying different limits would otherwise hash
// according to their wire order.
func TestCreateDelegationGrantStatement_DuplicateSpenderKeysAreOrderIndependent(t *testing.T) {
	first := &pb.DelegationSpender{
		SpenderIdentityPublicKey: pubkey(0xaa),
		PerTxCapSats:             100,
		RollingLimitSats:         500,
		RollingWindowSeconds:     86400,
	}
	second := &pb.DelegationSpender{
		SpenderIdentityPublicKey: pubkey(0xaa),
		PerTxCapSats:             900,
		RollingLimitSats:         5000,
		RollingWindowSeconds:     86400,
	}

	forward := baseGrant()
	forward.Spenders = []*pb.DelegationSpender{first, second}
	reversed := baseGrant()
	reversed.Spenders = []*pb.DelegationSpender{second, first}

	require.Equal(t,
		CreateDelegationGrantStatement(forward),
		CreateDelegationGrantStatement(reversed),
	)
}

// Same property for the install statement: a leaf id repeated with different
// delegate keys must not hash by wire order.
func TestCreateDecompositionInstallStatement_DuplicateLeafIDsAreOrderIndependent(t *testing.T) {
	first := &pb.LeafDecompositionInstall{
		LeafId:                   "11111111-1111-1111-1111-111111111111",
		DelegateSigningPublicKey: pubkey(0xaa),
	}
	second := &pb.LeafDecompositionInstall{
		LeafId:                   "11111111-1111-1111-1111-111111111111",
		DelegateSigningPublicKey: pubkey(0xbb),
	}

	grantID := "22222222-2222-2222-2222-222222222222"
	require.Equal(t,
		CreateDecompositionInstallStatement(grantID, []*pb.LeafDecompositionInstall{first, second}),
		CreateDecompositionInstallStatement(grantID, []*pb.LeafDecompositionInstall{second, first}),
	)
}
