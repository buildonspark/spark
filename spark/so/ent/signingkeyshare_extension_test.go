package ent

import (
	"maps"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"
)

func TestSumOfSigningKeyshares_DoesNotMutateInputs(t *testing.T) {
	firstSecret := keys.GeneratePrivateKey()
	first := &SigningKeyshare{
		SecretShare: &firstSecret,
		PublicKey:   keys.GeneratePrivateKey().Public(),
		PublicShares: map[string]keys.Public{
			"01": keys.GeneratePrivateKey().Public(),
			"02": keys.GeneratePrivateKey().Public(),
		},
	}
	secondSecret := keys.GeneratePrivateKey()
	second := &SigningKeyshare{
		SecretShare: &secondSecret,
		PublicKey:   keys.GeneratePrivateKey().Public(),
		PublicShares: map[string]keys.Public{
			"01": keys.GeneratePrivateKey().Public(),
			"02": keys.GeneratePrivateKey().Public(),
		},
	}

	originalFirstPublicShares := make(map[string]keys.Public, len(first.PublicShares))
	maps.Copy(originalFirstPublicShares, first.PublicShares)
	require.NotNil(t, first.SecretShare, "first keyshare secret share is nil")
	originalFirstSecretShare := *first.SecretShare
	originalFirstPublicKey := first.PublicKey

	sum, err := sumOfSigningKeyshares([]*SigningKeyshare{first, second})
	require.NoError(t, err)
	require.True(t, first.SecretShare.Equals(originalFirstSecretShare), "first keyshare secret share was mutated")
	require.True(t, first.PublicKey.Equals(originalFirstPublicKey), "first keyshare public key was mutated")

	for id, originalShare := range originalFirstPublicShares {
		require.True(t, first.PublicShares[id].Equals(originalShare), "first keyshare share %s was mutated", id)
		require.True(t, sum.PublicShares[id].Equals(originalShare.Add(second.PublicShares[id])), "sum share %s has unexpected value", id)
	}

	sumShareBefore := sum.PublicShares["01"]
	sum.PublicShares["01"] = sum.PublicShares["01"].Add(keys.GeneratePrivateKey().Public())
	require.True(t, first.PublicShares["01"].Equals(originalFirstPublicShares["01"]))
	require.False(t, sum.PublicShares["01"].Equals(sumShareBefore))
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

	v0a, v0b, v1 := int32(0), int32(0), int32(1)

	tests := []struct {
		name    string
		v1      *int32
		v2      *int32
		wantErr bool
		errSub  string
	}{
		{"same value different pointers", &v0a, &v0b, false, ""},
		{"one nil one non-nil", nil, &v1, true, "version mismatch"},
		{"both non-nil different values", &v0a, &v1, true, "version mismatch"},
		{"both nil", nil, nil, false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sumOfSigningKeyshares([]*SigningKeyshare{
				makeKeyshare(tc.v1),
				makeKeyshare(tc.v2),
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.errSub != "" {
				require.ErrorContains(t, err, tc.errSub)
			}
		})
	}
}
