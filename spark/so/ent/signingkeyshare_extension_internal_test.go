package ent

import (
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

	v0, v1 := int32(0), int32(1)
	sum, err := sumOfSigningKeyshares(t.Context(), []*SigningKeyshare{
		makeKeyshare(&v0),
		makeKeyshare(nil),
		makeKeyshare(&v1),
	})
	require.NoError(t, err)
	require.Nil(t, sum.SecretVersion)
}
