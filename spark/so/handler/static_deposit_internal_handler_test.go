package handler

import (
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"
)

func TestCreateArchiveStaticDepositAddressStatement(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{42})

	// Generate test keys
	testPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testPubKey := testPrivKey.Public()

	tests := []struct {
		name           string
		ownerPubKey    keys.Public
		network        btcnetwork.Network
		address        string
		expectedErrMsg string
	}{
		{
			name:           "valid inputs - mainnet",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Mainnet,
			address:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedErrMsg: "",
		},
		{
			name:           "valid inputs - regtest",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Regtest,
			address:        "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
			expectedErrMsg: "",
		},
		{
			name:           "valid inputs - testnet",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Testnet,
			address:        "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx",
			expectedErrMsg: "",
		},
		{
			name:           "zero public key",
			ownerPubKey:    keys.Public{},
			network:        btcnetwork.Mainnet,
			address:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedErrMsg: "owner identity public key cannot be zero",
		},
		{
			name:           "unspecified network",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Unspecified,
			address:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedErrMsg: "network cannot be unspecified",
		},
		{
			name:           "empty address",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Mainnet,
			address:        "",
			expectedErrMsg: "address cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := CreateArchiveStaticDepositAddressStatement(tt.ownerPubKey, tt.network, tt.address)

			if tt.expectedErrMsg != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.expectedErrMsg)
				require.Nil(t, hash)
			} else {
				require.NoError(t, err)
				require.NotNil(t, hash)
				require.Len(t, hash, 32, "hash should be 32 bytes (SHA256)")
			}
		})
	}
}

func TestCreateArchiveStaticDepositAddressStatement_KnownVector(t *testing.T) {
	// Test with a known private key to ensure consistent behavior
	privKeyHex := "3418d19f934d800fed3e364568e2d3a34d6574d7fa9459caea7c790e294651a9"
	privKeyBytes, err := hex.DecodeString(privKeyHex)
	require.NoError(t, err)

	privKey, err := keys.ParsePrivateKey(privKeyBytes)
	require.NoError(t, err)
	pubKey := privKey.Public()

	network := btcnetwork.Mainnet
	address := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"

	hash, err := CreateArchiveStaticDepositAddressStatement(pubKey, network, address)
	require.NoError(t, err)
	require.NotNil(t, hash)
	require.Len(t, hash, 32)

	// This is a regression test - the specific hash value ensures the implementation
	// doesn't change unexpectedly. If this test fails after intentional changes to
	// the hashing algorithm, update this expected value.
	expectedHashHex := "1d07f416c53021d26c32bee33ef5f03ba3b63b2399ea822e34a8d0e4109defb8"
	expectedHash, err := hex.DecodeString(expectedHashHex)
	require.NoError(t, err)

	require.Equal(t, expectedHash, hash, "hash should match known test vector")
	t.Logf("Hash verification successful: %x", hash)
}
