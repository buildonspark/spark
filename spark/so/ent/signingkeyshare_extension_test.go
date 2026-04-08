package ent

import (
	"maps"
	"testing"

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

func TestSumOfSigningKeyshares_ClearsSecretVersion(t *testing.T) {
	makeKeyshare := func(v *int32) *SigningKeyshare {
		priv := keys.GeneratePrivateKey()
		pub := priv.Public()
		return &SigningKeyshare{
			SecretShare:   &priv,
			SecretVersion: v,
			PublicShares:  map[string]keys.Public{"op": pub},
			PublicKey:     pub,
		}
	}

	v0, v1 := int32(0), int32(1)
	sum, err := sumOfSigningKeyshares([]*SigningKeyshare{
		makeKeyshare(&v0),
		makeKeyshare(nil),
		makeKeyshare(&v1),
	})
	require.NoError(t, err)
	require.Nil(t, sum.SecretVersion)
}
