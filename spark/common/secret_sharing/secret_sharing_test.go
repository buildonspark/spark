package secretsharing_test

import (
	"crypto/rand"
	mathrand "math/rand/v2"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretSharing(t *testing.T) {
	rng := mathrand.NewChaCha8([32]byte{})

	fieldModulus := secp256k1.S256().N
	secret, err := rand.Int(rng, fieldModulus)
	require.NoError(t, err)

	threshold := 3
	numberOfShares := 5

	shares, err := secretsharing.SplitSecret(secret, fieldModulus, threshold, numberOfShares)
	require.NoError(t, err)

	// Check that a threshold can reconstruct
	t.Run("ThresholdEnough", func(t *testing.T) {
		recoveredSecret, err := secretsharing.RecoverSecret(shares[:threshold])
		require.NoError(t, err)

		assert.Equal(t, secret, recoveredSecret, "secret %s does not match recovered secret %s", secret, recoveredSecret)
	})

	// Check that only a threshold can reconstruct
	t.Run("ThresholdRequired", func(t *testing.T) {
		_, err = secretsharing.RecoverSecret(shares[:threshold-1])
		require.Error(t, err, "should have failed to recover secret with less than a threshold")
	})
}

func TestVerifiableSecretSharing(t *testing.T) {
	rng := mathrand.NewChaCha8([32]byte{})

	fieldModulus := secp256k1.S256().N
	secret, err := rand.Int(rng, fieldModulus)
	require.NoError(t, err)

	threshold := 3
	numberOfShares := 5

	shares, err := secretsharing.SplitSecretWithProofs(secret, fieldModulus, threshold, numberOfShares)
	require.NoError(t, err)

	// Check that shares are valid and a threshold can reconstruct
	for _, share := range shares {
		err := secretsharing.ValidateShare(share)
		require.NoError(t, err)
	}

	recoveredSecret, err := secretsharing.RecoverSecret(shares[:threshold])
	require.NoError(t, err)

	assert.Equal(t, secret, recoveredSecret, "secret %s does not match recovered secret %s", secret, recoveredSecret)

	// Check that bad proof encodings are caught
	t.Run("CatchBadProofEncoding", func(t *testing.T) {
		shares[0].Proofs[0][0] ^= 255
		err := secretsharing.ValidateShare(shares[0])
		require.Error(t, err, "failed to catch bad proof encoding")
		shares[0].Proofs[0][0] ^= 255

		shares[1].Proofs[1][0] ^= 255
		err = secretsharing.ValidateShare(shares[1])
		require.Error(t, err, "failed to catch bad proof encoding")
		shares[1].Proofs[1][0] ^= 255
	})

	// Check that a share that doesn't match its proofs is caught
	t.Run("CatchWrongProof", func(t *testing.T) {
		shares[2].Share = shares[3].Share
		err := secretsharing.ValidateShare(shares[2])
		require.Error(t, err, "failed to catch share that doesn't match proofs")
	})
}

func TestSecretSharingBadPubkeyLen(t *testing.T) {
	rng := mathrand.NewChaCha8([32]byte{})

	fieldModulus := secp256k1.S256().N
	secret, err := rand.Int(rng, fieldModulus)
	require.NoError(t, err)

	threshold := 3
	numberOfShares := 1

	shares, err := secretsharing.SplitSecretWithProofs(secret, fieldModulus, threshold, numberOfShares)
	require.NoError(t, err)
	require.Len(t, shares, 1, "expected one share to be returned")

	share := shares[0]
	share.Proofs[0] = share.Proofs[0][:32]

	err = secretsharing.ValidateShare(share)
	require.ErrorContains(t, err, "malformed public key: invalid length: 32")
}

func TestMarshal(t *testing.T) {
	rng := mathrand.NewChaCha8([32]byte{})

	fieldModulus := secp256k1.S256().N
	secret, err := rand.Int(rng, fieldModulus)
	require.NoError(t, err)

	threshold := 3
	numberOfShares := 5

	shares, err := secretsharing.SplitSecretWithProofs(secret, fieldModulus, threshold, numberOfShares)
	require.NoError(t, err)

	marshaled0 := shares[0].MarshalProto()

	assert.Equal(t, shares[0].SecretShare.Share.Bytes(), marshaled0.SecretShare, "marshaled secret share does not match")
	assert.Equal(t, shares[0].Proofs, marshaled0.Proofs, "marshaled secret share proofs do not match")
}
