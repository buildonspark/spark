package dkg

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so"
)

var (
	fr    = rand.NewChaCha8([32]byte{1})
	priv1 = keys.MustGeneratePrivateKeyFromRand(fr)
	priv2 = keys.MustGeneratePrivateKeyFromRand(fr)
	priv3 = keys.MustGeneratePrivateKeyFromRand(fr)
)

func TestSignAndVerifyMessage(t *testing.T) {
	messageHash := sha256.Sum256([]byte("hello world"))
	priv := keys.MustGeneratePrivateKeyFromRand(fr)
	signatureBytes := signHash(priv, messageHash[:])

	sig, _ := ecdsa.ParseDERSignature(signatureBytes)

	assert.True(t, priv.Public().Verify(sig, messageHash[:]), "signature verification failed")
}

func TestRound1PackageHash(t *testing.T) {
	tests := []struct {
		name     string
		packages []map[string][]byte
		want     []byte
	}{
		{
			name:     "single package with one key",
			packages: []map[string][]byte{{"key1": priv1.Serialize()}},
			want:     mustDecodeHex(t, "bc22e1f564ac75c2b75313243e45eb3fa16323e433fb1e79bdf1dd1fdb584adb"),
		},
		{
			name: "single package with multiple keys",
			packages: []map[string][]byte{
				{"key1": priv1.Serialize(), "key2": priv2.Serialize(), "key3": priv3.Serialize()},
			},
			want: mustDecodeHex(t, "2e150e5aacd115b2b2a9a10a381f58f9377134993a8ed57171f690d45934bc58"),
		},
		{
			name: "multiple packages",
			packages: []map[string][]byte{
				{"key1": priv1.Serialize(), "key2": priv2.Serialize()},
				{"key3": priv3.Serialize()},
			},
			want: mustDecodeHex(t, "30549d126f1965926f8f5b45ebad70b8e1e256fd84b8f238ddf2110ef40fb8c6"),
		},
		{
			name:     "empty packages",
			packages: []map[string][]byte{},
			want:     mustDecodeHex(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := round1PackageHash(tt.packages)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRound1PackageHashPackageOrderSensitivity(t *testing.T) {
	tests := []struct {
		name     string
		packages []map[string][]byte
	}{
		{
			name: "two packages",
			packages: []map[string][]byte{
				{"key1": priv1.Serialize(), "key2": priv2.Serialize()},
				{"key3": priv3.Serialize()},
			},
		},
		{
			name: "three packages",
			packages: []map[string][]byte{
				{"key1": priv1.Serialize()},
				{"key2": priv2.Serialize()},
				{"key3": priv3.Serialize()},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := round1PackageHash(tt.packages)

			reversedPackages := append([]map[string][]byte{}, tt.packages...)
			slices.Reverse(reversedPackages)

			reversed := round1PackageHash(reversedPackages)
			assert.NotEqual(t, hash, reversed, "round1PackageHash() should be sensitive to package order")
		})
	}
}

func TestSignHash(t *testing.T) {
	hash := sha256.Sum256([]byte("test message"))

	signature := signHash(priv1, hash[:])
	sig, err := ecdsa.ParseDERSignature(signature)

	require.NoError(t, err)
	assert.True(t, priv1.Public().Verify(sig, hash[:]), "signHash() produced invalid signature")
}

func TestSignRound1Packages(t *testing.T) {
	packages := []map[string][]byte{
		{"key1": []byte("value1"), "key2": []byte("value2")},
		{"key3": []byte("value3")},
	}

	signature := signRound1Packages(priv1, packages)
	hash := round1PackageHash(packages)
	sig, err := ecdsa.ParseDERSignature(signature)

	require.NoError(t, err)
	assert.True(t, priv1.Public().Verify(sig, hash), "signRound1Packages() produced invalid signature")
}

func TestValidateRound1Signature(t *testing.T) {
	operator1 := &so.SigningOperator{IdentityPublicKey: priv1.Public()}
	operator2 := &so.SigningOperator{IdentityPublicKey: priv2.Public()}
	operatorMap := map[string]*so.SigningOperator{"op1": operator1, "op2": operator2}
	packages := []map[string][]byte{
		{"key1": []byte("value1")},
		{"key2": []byte("value2")},
	}
	sig1 := signRound1Packages(priv1, packages)
	sig2 := signRound1Packages(priv2, packages)
	signatures := map[string][]byte{"op1": sig1, "op2": sig2}

	valid, failures := validateRound1Signature(packages, signatures, operatorMap)

	assert.True(t, valid)
	assert.Empty(t, failures)
}

func TestValidateRound1Signature_InvalidSignature(t *testing.T) {
	operator1 := &so.SigningOperator{IdentityPublicKey: priv1.Public()}
	operator2 := &so.SigningOperator{IdentityPublicKey: priv2.Public()}
	operatorMap := map[string]*so.SigningOperator{"op1": operator1, "op2": operator2}
	packages := []map[string][]byte{
		{"key1": []byte("value1")},
		{"key2": []byte("value2")},
	}
	sig2 := signRound1Packages(priv2, packages)
	invalidSignatures := map[string][]byte{"op1": []byte("invalid"), "op2": sig2}

	valid, failures := validateRound1Signature(packages, invalidSignatures, operatorMap)

	assert.False(t, valid, "expected false for invalid signature")
	assert.Equal(t, []string{"op1"}, failures)
}

func TestRound2PackageHash(t *testing.T) {
	tests := []struct {
		name     string
		packages [][]byte
		want     []byte
	}{
		{
			name:     "single package",
			packages: [][]byte{[]byte("package1")},
			want:     mustDecodeHex(t, "73893d30923f338108486f1a6388bac31603db30e1b954a1ab6a77b1ab9d148d"),
		},
		{
			name:     "empty packages",
			packages: [][]byte{},
			want:     mustDecodeHex(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
		},
		{
			name:     "multiple packages",
			packages: [][]byte{[]byte("package1"), []byte("package2"), []byte("package3")},
			want:     mustDecodeHex(t, "48d8d70b79712c52dfa87860293ee867b61c9127e17c270a2da867975b82d527"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := round2PackageHash(tt.packages)
			assert.Equal(t, tt.want, hash)

			// Test package order sensitivity
			if len(tt.packages) > 1 {
				reversedPackages := append([][]byte{}, tt.packages...)
				slices.Reverse(reversedPackages)
				reversedHash := round2PackageHash(reversedPackages)
				assert.NotEqual(t, hash, reversedHash, "round2PackageHash() should be sensitive to package order")
			}
		})
	}
}

func TestSignRound2Packages(t *testing.T) {
	packages := [][]byte{[]byte("package1"), []byte("package2")}
	signature := signRound2Packages(priv1, packages)

	hash := round2PackageHash(packages)
	sig, err := ecdsa.ParseDERSignature(signature)

	require.NoError(t, err)
	assert.True(t, priv1.Public().Verify(sig, hash), "signRound2Packages() produced invalid signature")
}

func TestDeriveKeyIndex(t *testing.T) {
	baseID := uuid.Must(uuid.NewRandomFromReader(fr))
	tests := []struct {
		name  string
		index uint16
	}{
		{name: "index 0", index: 0},
		{name: "index 1", index: 1},
		{name: "index 65535", index: 65535},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			derived := deriveKeyIndex(baseID, tt.index)
			assert.Equal(t, baseID[:14], derived[:14], "deriveKeyIndex() modified first 14 bytes")

			// Verify the last 2 bytes contain the index
			derivedIndex := binary.BigEndian.Uint16(derived[14:])
			assert.Equal(t, tt.index, derivedIndex)
		})
	}
}

func mustDecodeHex(t *testing.T, s string) []byte {
	decoded, err := hex.DecodeString(s)
	require.NoError(t, err)
	return decoded
}
