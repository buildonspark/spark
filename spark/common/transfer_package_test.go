package common

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

func TestGetTransferPackageSigningPayload(t *testing.T) {
	transferID := uuid.New()

	mapToSign := map[string][]byte{
		"0000000000000000000000000000000000000000000000000000000000000002": {0x02},
		"0000000000000000000000000000000000000000000000000000000000000001": {0x01},
		"0000000000000000000000000000000000000000000000000000000000000003": {0x03},
	}
	transferPackage := &pb.TransferPackage{
		KeyTweakPackage: mapToSign,
	}

	payload := GetTransferPackageSigningPayload(transferID, transferPackage)

	hasher := sha256.New()
	hasher.Write(transferID[:])
	hasher.Write([]byte("0000000000000000000000000000000000000000000000000000000000000001:"))
	hasher.Write([]byte{0x01})
	hasher.Write([]byte(";"))
	hasher.Write([]byte("0000000000000000000000000000000000000000000000000000000000000002:"))
	hasher.Write([]byte{0x02})
	hasher.Write([]byte(";"))
	hasher.Write([]byte("0000000000000000000000000000000000000000000000000000000000000003:"))
	hasher.Write([]byte{0x03})
	hasher.Write([]byte(";"))
	require.Equal(t, hasher.Sum(nil), payload)
}

func packageWithIntent(intent *pb.DelegationIntent) *pb.TransferPackage {
	return &pb.TransferPackage{
		HashVariant: pb.HashVariant_HASH_VARIANT_V2,
		KeyTweakPackage: map[string][]byte{
			"0000000000000000000000000000000000000000000000000000000000000001": {0x01},
			"0000000000000000000000000000000000000000000000000000000000000002": {0x02},
		},
		DelegationIntent: intent,
	}
}

// baseDelegationIntent returns a fixed intent so two independent constructions
// hash identically; tests that need a change mutate a single field.
func baseDelegationIntent() *pb.DelegationIntent {
	return &pb.DelegationIntent{
		GrantId:                  "11111111-1111-1111-1111-111111111111",
		SpenderIdentityPublicKey: make([]byte, 33),
		TotalAmountSats:          1000,
		ReceiverAmountsSats: map[string]uint64{
			"02aa": 600,
			"02bb": 400,
		},
	}
}

func TestGetTransferPackageSigningPayloadDelegationDeterministic(t *testing.T) {
	transferID := uuid.New()
	intent := baseDelegationIntent()

	// Same input hashes to the same payload across independent constructions
	// (map iteration order must not matter).
	first := GetTransferPackageSigningPayload(transferID, packageWithIntent(intent))
	second := GetTransferPackageSigningPayload(transferID, packageWithIntent(baseDelegationIntent()))
	require.Equal(t, first, second)

	// A populated intent must not collide with the no-intent case.
	nilIntentPayload := GetTransferPackageSigningPayload(transferID, packageWithIntent(nil))
	require.NotEqual(t, nilIntentPayload, first)
}

// The intent fields are appended only when an intent is present, so a transfer
// without one must hash exactly as it did before delegation existed. If this
// fails, every already-released SDK stops verifying against this operator.
func TestTransferPackageWithoutIntentHashesAsPlainV2(t *testing.T) {
	transferID := uuid.New()

	withNilIntent := packageWithIntent(nil)
	noIntentField := &pb.TransferPackage{
		HashVariant:     pb.HashVariant_HASH_VARIANT_V2,
		KeyTweakPackage: withNilIntent.GetKeyTweakPackage(),
	}

	require.Equal(t,
		GetTransferPackageSigningPayload(transferID, noIntentField),
		GetTransferPackageSigningPayload(transferID, withNilIntent),
	)
}

// Presence decides the meaning of the signature, so an intent that is set but
// empty is a different signed statement from no intent at all. The TypeScript
// signer must agree with this exactly; see the mirror in transfer_package.test.ts.
func TestTransferPackageEmptyIntentDiffersFromAbsentIntent(t *testing.T) {
	transferID := uuid.New()

	absent := GetTransferPackageSigningPayload(transferID, packageWithIntent(nil))
	empty := GetTransferPackageSigningPayload(transferID, packageWithIntent(&pb.DelegationIntent{}))

	require.NotEqual(t, absent, empty)
}

func TestPayloadBindsDelegationIntentFields(t *testing.T) {
	transferID := uuid.New()
	base := GetTransferPackageSigningPayload(transferID, packageWithIntent(baseDelegationIntent()))

	tests := []struct {
		name   string
		mutate func(*pb.DelegationIntent)
	}{
		{"grant_id", func(i *pb.DelegationIntent) { i.GrantId = uuid.New().String() }},
		{"spender_identity_public_key", func(i *pb.DelegationIntent) {
			pk := make([]byte, 33)
			pk[0] = 0x02
			i.SpenderIdentityPublicKey = pk
		}},
		{"total_amount_sats", func(i *pb.DelegationIntent) { i.TotalAmountSats = 1001 }},
		{"receiver_amount_value", func(i *pb.DelegationIntent) { i.ReceiverAmountsSats["02aa"] = 601 }},
		{"receiver_amount_key", func(i *pb.DelegationIntent) {
			i.ReceiverAmountsSats = map[string]uint64{"02aa": 600, "02cc": 400}
		}},
		{"receiver_amount_added", func(i *pb.DelegationIntent) {
			i.ReceiverAmountsSats = map[string]uint64{"02aa": 600, "02bb": 400, "02dd": 1}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := baseDelegationIntent()
			tt.mutate(intent)
			mutated := GetTransferPackageSigningPayload(transferID, packageWithIntent(intent))
			require.NotEqual(t, base, mutated, "flipping %s must change the signing payload", tt.name)
		})
	}
}
