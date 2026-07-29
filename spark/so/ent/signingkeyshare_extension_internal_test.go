package ent

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"
)

func TestSumOfSigningKeyshares_DoesNotMutateInputs(t *testing.T) {
	secretA := keys.GeneratePrivateKey()
	secretB := keys.GeneratePrivateKey()

	shareA1 := keys.GeneratePrivateKey().Public()
	shareA2 := keys.GeneratePrivateKey().Public()
	shareB1 := keys.GeneratePrivateKey().Public()
	shareB2 := keys.GeneratePrivateKey().Public()

	keyshare1 := &SigningKeyshare{
		ID:           uuid.New(),
		SecretShare:  &secretA,
		PublicShares: map[string]keys.Public{"1": shareA1, "2": shareA2},
		PublicKey:    secretA.Public(),
	}
	keyshare2 := &SigningKeyshare{
		ID:           uuid.New(),
		SecretShare:  &secretB,
		PublicShares: map[string]keys.Public{"1": shareB1, "2": shareB2},
		PublicKey:    secretB.Public(),
	}

	original1 := map[string]keys.Public{"1": keyshare1.PublicShares["1"], "2": keyshare1.PublicShares["2"]}
	original2 := map[string]keys.Public{"1": keyshare2.PublicShares["1"], "2": keyshare2.PublicShares["2"]}

	_, err := sumOfSigningKeyshares(t.Context(), []*SigningKeyshare{keyshare1, keyshare2})
	require.NoError(t, err)

	require.Equal(t, original1, keyshare1.PublicShares)
	require.Equal(t, original2, keyshare2.PublicShares)
}

func TestSumOfSigningKeyshares_SecretVersionComparison(t *testing.T) {
	makeKeyshare := func(v *int32) *SigningKeyshare {
		priv := keys.GeneratePrivateKey()
		pub := priv.Public()
		return &SigningKeyshare{
			ID:            uuid.New(),
			SecretShare:   &priv,
			SecretVersion: v,
			PublicShares:  map[string]keys.Public{"op": pub},
			PublicKey:     pub,
		}
	}

	sum, err := sumOfSigningKeyshares(t.Context(), []*SigningKeyshare{
		makeKeyshare(new(int32(0))),
		makeKeyshare(nil),
		makeKeyshare(new(int32(1))),
	})
	require.NoError(t, err)
	require.Nil(t, sum.SecretVersion)
}

// Tested below the rotation entry points because the overflow boundary is only
// reachable from a keyshare already sitting at math.MaxInt32, which no sequence
// of public rotations can produce. The version-allocation quadrants themselves
// are additionally covered end-to-end by the rotation tests.
func TestNextSigningKeyshareSecretVersion(t *testing.T) {
	tests := []struct {
		name            string
		ephemeralLatest *int32
		base            *int32
		expectedVersion int32
		expectedErr     bool
	}{
		{
			name:            "no ephemeral rows and no base starts at zero",
			expectedVersion: 0,
		},
		{
			name:            "ephemeral rows and no base advance past the ephemeral latest",
			ephemeralLatest: new(int32(5)),
			expectedVersion: 6,
		},
		{
			name:            "ephemeral latest of zero and no base advances to one",
			ephemeralLatest: new(int32(0)),
			expectedVersion: 1,
		},
		{
			name:            "no ephemeral rows advances past the base instead of restarting at zero",
			base:            new(int32(7)),
			expectedVersion: 8,
		},
		{
			name:            "no ephemeral rows and a base of zero advances to one",
			base:            new(int32(0)),
			expectedVersion: 1,
		},
		{
			name:            "ephemeral latest ahead of the base advances past the ephemeral latest",
			ephemeralLatest: new(int32(9)),
			base:            new(int32(4)),
			expectedVersion: 10,
		},
		{
			name:            "ephemeral latest level with the base advances past both",
			ephemeralLatest: new(int32(4)),
			base:            new(int32(4)),
			expectedVersion: 5,
		},
		{
			// SP-3668: allocating ephemeralLatest+1 here would collide with the
			// base version the commit hook retires, deleting the new row.
			name:            "ephemeral latest one behind the base advances past the base",
			ephemeralLatest: new(int32(3)),
			base:            new(int32(4)),
			expectedVersion: 5,
		},
		{
			name:            "ephemeral latest far behind the base advances past the base",
			ephemeralLatest: new(int32(0)),
			base:            new(int32(12)),
			expectedVersion: 13,
		},
		{
			name:            "ephemeral latest at the maximum overflows",
			ephemeralLatest: new(int32(math.MaxInt32)),
			expectedErr:     true,
		},
		{
			name:        "base at the maximum overflows",
			base:        new(int32(math.MaxInt32)),
			expectedErr: true,
		},
		{
			name:            "ephemeral latest at the maximum overflows even with a low base",
			ephemeralLatest: new(int32(math.MaxInt32)),
			base:            new(int32(1)),
			expectedErr:     true,
		},
		{
			name:            "base at the maximum overflows even with a low ephemeral latest",
			ephemeralLatest: new(int32(1)),
			base:            new(int32(math.MaxInt32)),
			expectedErr:     true,
		},
		{
			name:            "both inputs at the maximum overflow",
			ephemeralLatest: new(int32(math.MaxInt32)),
			base:            new(int32(math.MaxInt32)),
			expectedErr:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := uuid.New()

			version, err := nextSigningKeyshareSecretVersion(id, test.ephemeralLatest, test.base)

			if test.expectedErr {
				require.ErrorContains(t, err, id.String())
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.expectedVersion, version)
			if test.base != nil {
				require.Greater(t, version, *test.base, "allocated version must not collide with the retired base")
			}
			if test.ephemeralLatest != nil {
				require.Greater(t, version, *test.ephemeralLatest, "allocated version must not collide with an existing ephemeral row")
			}
		})
	}
}
