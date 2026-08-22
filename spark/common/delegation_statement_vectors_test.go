package common

import (
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// These frozen vectors pin the canonical statement bytes for every Spark Pull
// (delegated spending) lifecycle operation, plus the transfer-package signing
// payload for a delegated transfer.
//
// The TypeScript signing ports do not exist yet at this commit: they arrive with
// the SDK core (B8a), which adds utils/delegation-statements.ts, the delegation
// intent arm of transfer_package.ts, and a delegation-statements.test.ts
// asserting these same hex strings. Until then these vectors bind the Go side
// only, and nothing cross-checks a TS signer against them.
//
// Regenerating: if a statement's canonical encoding legitimately changes, run
// this test with -v to print the new vectors, update the constants here AND the
// mirror in the TS test once it exists.

func vectorPubkey(b byte) []byte {
	pk := make([]byte, 33)
	pk[0] = 0x02
	pk[32] = b
	return pk
}

func vectorGrant() *pb.DelegationGrant {
	return &pb.DelegationGrant{
		GrantId:                       "11111111-1111-1111-1111-111111111111",
		OwnerIdentityPublicKey:        vectorPubkey(0x01),
		Network:                       pb.Network_REGTEST,
		ExpiryTime:                    &timestamppb.Timestamp{Seconds: 1_800_000_000, Nanos: 7_000_000},
		ScopeTransfer:                 true,
		ScopeRenew:                    false,
		ScopeClaim:                    true,
		FeeFlatSats:                   10,
		FeeCollectorIdentityPublicKey: vectorPubkey(0x09),
		Version:                       3,
		Spenders: []*pb.DelegationSpender{
			{SpenderIdentityPublicKey: vectorPubkey(0xaa), PerTxCapSats: 100, RollingLimitSats: 500, RollingWindowSeconds: 86400},
			{SpenderIdentityPublicKey: vectorPubkey(0xbb), PerTxCapSats: 200, RollingLimitSats: 900, RollingWindowSeconds: 3600},
		},
	}
}

// vectorGrantUnlimited is vectorGrant with the first spender's ceilings waived:
// both unlimited flags set and both caps zero (the canonical encoding).
func vectorGrantUnlimited() *pb.DelegationGrant {
	g := vectorGrant()
	g.Spenders[0].PerTxCapSats = 0
	g.Spenders[0].RollingLimitSats = 0
	g.Spenders[0].PerTxUnlimited = true
	g.Spenders[0].RollingUnlimited = true
	return g
}

func TestDelegationStatementFrozenVectors(t *testing.T) {
	// Mirrored byte-for-byte in the TS SDK test delegation-statements.test.ts.
	// grantVec / spenderAddVec are v2 statements (domain tag "v2", spender tuple
	// binds the unlimited flags); the *Unlimited vectors pin the flags-true
	// encoding. revoke / install statements are unchanged v1.
	const (
		grantVec               = "b8983bb53231881ab72d8f25d8517f9121b498a3e78808096a7c440beb26890e"
		grantUnlimitedVec      = "31ee9276a83a4eef7cefeb4850e8ff1906f08a9d9e1121a2746af7e7e0db6602"
		revokeVec              = "f898bdf0647048acd9472cdbcd9a80e8ca145f84c657109ff326d889a8d59148"
		spenderAddVec          = "3c84419371a2fefb4cbf17637e37493ac8a7c20d01d59da1a926aa10bc004d36"
		spenderAddUnlimitedVec = "dd5623987c37fc27017e1e7c859170ac7585712b67eb6fce651946335ec66d91"
		spenderRevokeVec       = "1a05b9d401de534b61fa7ab1de0bc8a3ab73da5e6c1815217654a7e61aa11560"
		installVec             = "0a9528ffa128921fc0ce1421f0a5565d5b5f3b0f1606472db469a2472d078ed3"
		transferWithIntentVec  = "0e4e193bde433332d25d3a7aa9b892cae4f9e434c4e653f9252307569b8ed806"
	)

	grant := hex.EncodeToString(CreateDelegationGrantStatement(vectorGrant()))
	grantUnlimited := hex.EncodeToString(CreateDelegationGrantStatement(vectorGrantUnlimited()))
	revoke := hex.EncodeToString(CreateDelegationRevokeStatement("11111111-1111-1111-1111-111111111111", 4, vectorPubkey(0x01)))
	spenderAdd := hex.EncodeToString(CreateDelegationSpenderAddStatement(
		"11111111-1111-1111-1111-111111111111", 5,
		&pb.DelegationSpender{SpenderIdentityPublicKey: vectorPubkey(0xaa), PerTxCapSats: 100, RollingLimitSats: 500, RollingWindowSeconds: 86400},
	))
	spenderAddUnlimited := hex.EncodeToString(CreateDelegationSpenderAddStatement(
		"11111111-1111-1111-1111-111111111111", 5,
		&pb.DelegationSpender{SpenderIdentityPublicKey: vectorPubkey(0xaa), RollingWindowSeconds: 86400, PerTxUnlimited: true, RollingUnlimited: true},
	))
	spenderRevoke := hex.EncodeToString(CreateDelegationSpenderRevokeStatement("11111111-1111-1111-1111-111111111111", 6, vectorPubkey(0xaa)))
	install := hex.EncodeToString(CreateDecompositionInstallStatement("11111111-1111-1111-1111-111111111111", []*pb.LeafDecompositionInstall{
		{LeafId: "22222222-2222-2222-2222-222222222222", DelegateSigningPublicKey: vectorPubkey(0x42)},
		{LeafId: "33333333-3333-3333-3333-333333333333", DelegateSigningPublicKey: vectorPubkey(0x43)},
	}))

	transferID, err := uuid.Parse("44444444-4444-4444-4444-444444444444")
	require.NoError(t, err)
	pkg := &pb.TransferPackage{
		KeyTweakPackage: map[string][]byte{"1": {0xde, 0xad}, "2": {0xbe, 0xef}},
		HashVariant:     pb.HashVariant_HASH_VARIANT_V2,
		DelegationIntent: &pb.DelegationIntent{
			GrantId:                  "11111111-1111-1111-1111-111111111111",
			SpenderIdentityPublicKey: vectorPubkey(0xaa),
			TotalAmountSats:          1234,
			ReceiverAmountsSats:      map[string]uint64{"deadbeef": 1000, "cafe": 234},
		},
	}
	transferWithIntent := hex.EncodeToString(GetTransferPackageSigningPayload(transferID, pkg))

	require.Equal(t, grantVec, grant, "grant statement vector changed")
	require.Equal(t, grantUnlimitedVec, grantUnlimited, "unlimited grant statement vector changed")
	require.Equal(t, revokeVec, revoke, "revoke statement vector changed")
	require.Equal(t, spenderAddVec, spenderAdd, "spender-add statement vector changed")
	require.Equal(t, spenderAddUnlimitedVec, spenderAddUnlimited, "unlimited spender-add statement vector changed")
	require.Equal(t, spenderRevokeVec, spenderRevoke, "spender-revoke statement vector changed")
	require.Equal(t, installVec, install, "install statement vector changed")
	require.Equal(t, transferWithIntentVec, transferWithIntent, "delegated transfer payload vector changed")
}
