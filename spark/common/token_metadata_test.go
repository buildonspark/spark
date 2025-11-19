package common

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createValidTokenMetadata(rng *rand.ChaCha8) *TokenMetadata {
	return &TokenMetadata{
		IssuerPublicKey:         keys.MustGeneratePrivateKeyFromRand(rng).Public(),
		TokenName:               "Test Token",
		TokenTicker:             "TEST",
		Decimals:                8,
		MaxSupply:               make([]byte, 16),
		IsFreezable:             true,
		CreationEntityPublicKey: keys.Public{},
		Network:                 btcnetwork.Regtest,
	}
}

func createValidTokenMetadataWithExtensions(rng *rand.ChaCha8) *TokenMetadata {
	tokenMetadata := createValidTokenMetadata(rng)
	tokenMetadata.ExtraMetadata = make([]byte, MaxExtraMetadataLength)
	return tokenMetadata
}

func TestTokenMetadata_Validate(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	t.Run("valid metadata", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		err := tm.Validate()
		if err != nil {
			t.Errorf("expected no error for valid metadata, got: %v", err)
		}
		_, err = tm.ComputeTokenIdentifier()
		if err != nil {
			t.Errorf("expected no error computing identifier, got: %v", err)
		}
	})

	t.Run("valid non-ascii token name", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		tm.TokenName = "tεst" // 4 runes, 5 bytes
		err := tm.Validate()
		if err != nil {
			t.Errorf("expected no error for non-ascii token name, got: %v", err)
		}
		_, err = tm.ComputeTokenIdentifier()
		if err != nil {
			t.Errorf("expected no error computing identifier, got: %v", err)
		}
	})

	t.Run("valid non-ascii ticker", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		tm.TokenTicker = "tεst" // 4 runes, 5 bytes
		err := tm.Validate()
		if err != nil {
			t.Errorf("expected no error for non-ascii ticker, got: %v", err)
		}
	})

	t.Run("invalid issuer public key", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		tm.IssuerPublicKey = keys.Public{}
		err := tm.Validate()
		if !errors.Is(err, ErrInvalidIssuerPublicKey) {
			t.Errorf("expected error about issuer public key length, got: %v", err)
		}
	})

	t.Run("invalid token name length ascii characters", func(t *testing.T) {
		testCases := []struct {
			name      string
			tokenName string
			expectErr error
		}{
			{"empty", "", ErrTokenNameEmpty},
			{"too short", "ab", ErrTokenNameLength},
			{"too long", "a very long token name that is over twenty characters", ErrTokenNameLength},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.TokenName = tc.tokenName
				err := tm.Validate()
				if err == nil {
					t.Errorf("expected error for token name '%s', got none", tc.tokenName)
				}
				if !errors.Is(err, tc.expectErr) {
					t.Errorf("expected error containing '%s', got: %v", tc.expectErr, err)
				}
			})
		}
	})

	t.Run("invalid token ticker length ascii characters", func(t *testing.T) {
		testCases := []struct {
			name        string
			tokenTicker string
			expectErr   error
		}{
			{"empty", "", ErrTokenTickerEmpty},
			{"too short", "ab", ErrTokenTickerLength},
			{"too long", "toolong", ErrTokenTickerLength},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.TokenTicker = tc.tokenTicker
				err := tm.Validate()
				if err == nil {
					t.Errorf("expected error for token ticker '%s', got none", tc.tokenTicker)
				}
				if !errors.Is(err, tc.expectErr) {
					t.Errorf("expected error containing '%s', got: %v", tc.expectErr, err)
				}
			})
		}
	})

	t.Run("invalid UTF-8 in token name", func(t *testing.T) {
		testCases := []struct {
			name      string
			tokenName string
		}{
			{"invalid UTF-8 sequence", string([]byte{0xFF, 0x80, 0x80})},
			{"overlong encoding", string([]byte{0xC0, 0x80, 0x80})},
			{"incomplete sequence", string([]byte{0xC2})},
			{"invalid continuation byte", string([]byte{0x80, 0x80, 0x80})},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.TokenName = tc.tokenName
				err := tm.Validate()
				if err == nil {
					t.Errorf("expected error for invalid UTF-8 token name, got none")
				}
				if !errors.Is(err, ErrTokenNameUTF8) {
					t.Errorf("expected error about invalid UTF-8 in token name, got: %v", err)
				}
			})
		}
	})

	t.Run("invalid UTF-8 in token ticker", func(t *testing.T) {
		testCases := []struct {
			name        string
			tokenTicker string
		}{
			{"invalid UTF-8 sequence", string([]byte{0xFF, 0x80, 0x80})},
			{"overlong encoding", string([]byte{0xC0, 0x80, 0x80})},
			{"incomplete sequence", string([]byte{0xC2})},
			{"invalid continuation byte", string([]byte{0x80, 0x80, 0x80})},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.TokenTicker = tc.tokenTicker
				err := tm.Validate()
				if err == nil {
					t.Errorf("expected error for invalid UTF-8 token ticker, got none")
				}
				if !errors.Is(err, ErrTokenTickerUTF8) {
					t.Errorf("expected error about invalid UTF-8 in token ticker, got: %v", err)
				}
			})
		}
	})

	t.Run("non-normalized UTF-8 in token name", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		tm.TokenName = "te\u0301st" // "e" + combining accent for "é"
		err := tm.Validate()
		if err == nil {
			t.Errorf("expected error for non-normalized UTF-8 token name, got none")
		}
		if !errors.Is(err, ErrTokenNameUTF8) {
			t.Errorf("expected error about non-normalized UTF-8 in token name, got: %v", err)
		}
	})

	t.Run("non-normalized UTF-8 in token ticker", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		tm.TokenTicker = "te\u0301st" // "e" + combining accent for "é"
		err := tm.Validate()
		if err == nil {
			t.Errorf("expected error for non-normalized UTF-8 token ticker, got none")
		}
		if !errors.Is(err, ErrTokenTickerUTF8) {
			t.Errorf("expected error about non-normalized UTF-8 in token ticker, got: %v", err)
		}
	})

	t.Run("valid UTF-8 with unicode characters", func(t *testing.T) {
		testCases := []struct {
			name        string
			tokenName   string
			tokenTicker string
		}{
			{"short unicode", "tëst", "tëst"},       // ë is 2 bytes, total 5 bytes for name, 5 bytes for ticker
			{"accented characters", "café", "café"}, // é is 2 bytes, total 5 bytes each
			{"mixed scripts", "test测", "test"},      // 测 is 3 bytes, total 7 bytes for name, 4 bytes for ticker
			{"currency symbols", "€test", "€tc"},    // € is 3 bytes, total 8 bytes for name, 5 bytes for ticker
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.TokenName = tc.tokenName
				tm.TokenTicker = tc.tokenTicker
				err := tm.Validate()
				if err != nil {
					t.Errorf("expected no error for valid UTF-8 characters, got: %v", err)
				}
				// Test identifier computation for valid UTF-8
				_, err = tm.ComputeTokenIdentifier()
				if err != nil {
					t.Errorf("expected no error computing identifier, got: %v", err)
				}
			})
		}
	})

	t.Run("invalid max supply length", func(t *testing.T) {
		testCases := []struct {
			name   string
			length int
		}{
			{"empty", 0},
			{"too short", 15},
			{"too long", 17},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.MaxSupply = make([]byte, tc.length)
				err := tm.Validate()
				if err == nil {
					t.Errorf("expected error for max supply length %d, got none", tc.length)
				}
				if !errors.Is(err, ErrInvalidMaxSupplyLength) {
					t.Errorf("expected error about max supply length, got: %v", err)
				}
			})
		}
	})

	t.Run("UTF-8 byte length validation", func(t *testing.T) {
		testCases := []struct {
			name        string
			tokenName   string
			tokenTicker string
			expectError bool
			errorType   error
		}{
			{
				name:        "emoji exceeds byte limit for ticker",
				tokenName:   "Test",
				tokenTicker: "🚀🚀", // 2 emojis = 8 bytes, exceeds 6 byte limit
				expectError: true,
				errorType:   ErrTokenTickerLength,
			},
			{
				name:        "emoji within byte limit for ticker",
				tokenName:   "Test",
				tokenTicker: "🚀", // 1 emoji = 4 bytes, within limit but needs to be exactly 3-6 bytes
				expectError: false,
			},
			{
				name:        "long unicode name exceeds byte limit",
				tokenName:   "🚀🚀🚀🚀🚀🚀", // 6 emojis = 24 bytes, exceeds 20 byte limit
				tokenTicker: "TEST",
				expectError: true,
				errorType:   ErrTokenNameLength,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.TokenName = tc.tokenName
				tm.TokenTicker = tc.tokenTicker
				err := tm.Validate()

				if tc.expectError {
					if err == nil {
						t.Errorf("expected error, got none")
					} else if !errors.Is(err, tc.errorType) {
						t.Errorf("expected error type %v, got: %v", tc.errorType, err)
					}
				} else {
					if err != nil {
						t.Errorf("expected no error, got: %v", err)
					}
				}
			})
		}
	})

	t.Run("minimum boundary values", func(t *testing.T) {
		tm := &TokenMetadata{
			IssuerPublicKey:         keys.MustGeneratePrivateKeyFromRand(rng).Public(),
			TokenName:               "abc", // exactly 3 bytes (minimum)
			TokenTicker:             "abc", // exactly 3 bytes (minimum)
			Decimals:                0,     // minimum allowed
			MaxSupply:               make([]byte, 16),
			IsFreezable:             false,
			CreationEntityPublicKey: keys.Public{},
			Network:                 btcnetwork.Regtest,
		}

		err := tm.Validate()
		if err != nil {
			t.Errorf("expected no error for boundary values, got: %v", err)
		}

		// Should also be able to compute identifier
		_, err = tm.ComputeTokenIdentifier()
		if err != nil {
			t.Errorf("expected no error computing identifier, got: %v", err)
		}
	})

	t.Run("maximum boundary values", func(t *testing.T) {
		tm := &TokenMetadata{
			IssuerPublicKey: keys.MustGeneratePrivateKeyFromRand(rng).Public(),
			TokenName:       "12345678901234567890", // exactly 20 bytes (maximum)
			TokenTicker:     "MAXLEN",               // exactly 6 bytes (maximum)
			Decimals:        255,                    // maximum allowed
			MaxSupply: []byte{
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			}, // max uint128
			IsFreezable:             true,
			CreationEntityPublicKey: keys.Public{},
			Network:                 btcnetwork.Regtest,
			ExtraMetadata:           make([]byte, MaxExtraMetadataLength),
		}

		err := tm.Validate()
		if err != nil {
			t.Errorf("expected no error for maximum boundary values, got: %v", err)
		}
		_, err = tm.ComputeTokenIdentifier()
		if err != nil {
			t.Errorf("expected no error computing identifier, got: %v", err)
		}
	})

	t.Run("Extra metadata byte length validation", func(t *testing.T) {
		testCases := []struct {
			name          string
			extraMetadata []byte
			expectError   bool
		}{
			{
				name:          "valid extra metadata length",
				extraMetadata: make([]byte, MaxExtraMetadataLength),
				expectError:   false,
			},
			{
				name:          "extra metadata length is less than limit",
				extraMetadata: make([]byte, MaxExtraMetadataLength-1),
				expectError:   false,
			},
			{
				name:          "extra metadata length exceeds limit",
				extraMetadata: make([]byte, MaxExtraMetadataLength+1),
				expectError:   true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				tm := createValidTokenMetadata(rng)
				tm.ExtraMetadata = tc.extraMetadata
				err := tm.Validate()

				if tc.expectError {
					if err == nil {
						t.Errorf("expected error, got none")
					}
				} else {
					if err != nil {
						t.Errorf("expected no error, got: %v", err)
					}
				}
			})
		}
	})
}

// Confirm that l1 token identifier computation continues to match for a live l1 spark token
// and does not change in a future push.
func TestActualProductionL1TokenIdentifier(t *testing.T) {
	issuerPubKey := keys.MustParsePublicKeyHex("036898ed2b633947f0994b8952fa06da2cfc7d1ee003fcf2cc076752b9ad3b3691")
	maxSupply, _ := hex.DecodeString("00000000000000000000000000009c3f")

	// This is an actual token created in production servers on Regtest.
	tm := &TokenMetadata{
		IssuerPublicKey:         issuerPubKey,
		TokenName:               "RaccoonCoin",
		TokenTicker:             "RCC",
		Decimals:                10,
		MaxSupply:               maxSupply,
		IsFreezable:             false,
		CreationEntityPublicKey: keys.Public{},
		Network:                 btcnetwork.Regtest,
	}

	tokenIdentifier, err := tm.ComputeTokenIdentifier()
	require.NoError(t, err)

	// IMPORTANT: This expected value should never change!
	expectedIdentifier, _ := hex.DecodeString("f1ca1e65691d0f65132ce24608594aaccd741e323056c97407a9f625b0ee4251")
	assert.EqualValues(t, expectedIdentifier, tokenIdentifier)
}

// Confirm that spark token identifier computation continues to match for a live l1 spark token
// and does not change in a future push.
func TestActualProductionSparkTokenIdentifier(t *testing.T) {
	issuerPubKey := keys.MustParsePublicKeyHex("036898ed2b633947f0994b8952fa06da2cfc7d1ee003fcf2cc076752b9ad3b3691")
	creationEntityPublicKey := keys.MustParsePublicKeyHex("0345b806679a5e63159584db91fec038cffd2ef59cee031abe92e2f30bf0642175")
	maxSupply, _ := hex.DecodeString("00000000000000000000000000009c3f")

	// This is an actual token created in production servers on Regtest.
	tm := &TokenMetadata{
		IssuerPublicKey:         issuerPubKey,
		TokenName:               "RaccoonCoin",
		TokenTicker:             "RCC",
		Decimals:                10,
		MaxSupply:               maxSupply,
		IsFreezable:             false,
		CreationEntityPublicKey: creationEntityPublicKey,
		Network:                 btcnetwork.Regtest,
	}

	tokenIdentifier, err := tm.ComputeTokenIdentifier()
	require.NoError(t, err)

	// IMPORTANT: This expected value should never change!
	expectedIdentifier, _ := hex.DecodeString("8b5fde73c803f6ef5c819ae94ddd035f02bee63555a08fc94f6851e289b46a1b")
	assert.EqualValues(t, expectedIdentifier, tokenIdentifier)
}

func TestTokenMetadata_ComputeTokenIdentifier(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	// This initial test ensures that a valid metadata object produces a hash of the correct length.
	t.Run("valid metadata produces hash", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		hash, err := tm.ComputeTokenIdentifier()
		require.NoError(t, err)
		assert.Len(t, hash, 32) // SHA256 produces 32-byte hash
	})

	// This test ensures that a metadata object with extensions produces a hash of the correct length.
	t.Run("valid metadata with extensions produces hash", func(t *testing.T) {
		tm := createValidTokenMetadataWithExtensions(rng)
		hash, err := tm.ComputeTokenIdentifier()
		require.NoError(t, err)
		assert.Len(t, hash, 32)
	})

	// This test ensures that a base metadata object produces a different hash than a similar metadata object with extensions.
	t.Run("valid base metadata produces different hash from base metadata with extensions", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		hashBase, err := tm.ComputeTokenIdentifier()
		require.NoError(t, err)
		assert.Len(t, hashBase, 32)

		tm.ExtraMetadata = make([]byte, MaxExtraMetadataLength)
		hashWithExtensions, err := tm.ComputeTokenIdentifier()
		require.NoError(t, err)
		assert.Len(t, hashWithExtensions, 32)
		assert.NotEqual(t, hashBase, hashWithExtensions)
	})

	// This test case handles invalid metadata and ensures it returns the expected error.
	t.Run("invalid metadata returns error", func(t *testing.T) {
		tm := createValidTokenMetadata(rng)
		tm.IssuerPublicKey = keys.Public{} // invalid length

		_, err := tm.ComputeTokenIdentifier()
		require.ErrorIs(t, err, ErrInvalidTokenMetadata)
	})

	// These test cases compare the hashes of two metadata objects that are modified in some way.
	testCases := []struct {
		name          string
		modifier1     func(tm *TokenMetadata)
		modifier2     func(tm *TokenMetadata)
		shouldBeEqual bool
	}{
		{
			name: "same input produces same hash",
			modifier1: func(tm *TokenMetadata) {
				tm.Network = btcnetwork.Mainnet
			},
			modifier2: func(tm *TokenMetadata) {
				tm.Network = btcnetwork.Mainnet
			},
			shouldBeEqual: true,
		},
		{
			name: "different networks produce different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.Network = btcnetwork.Regtest
			},
			modifier2: func(tm *TokenMetadata) {
				tm.Network = btcnetwork.Mainnet
			},
			shouldBeEqual: false,
		},
		{
			name: "different token names produce different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.TokenName = "Token One"
			},
			modifier2: func(tm *TokenMetadata) {
				tm.TokenName = "Token Two"
			},
			shouldBeEqual: false,
		},
		{
			name: "different token tickers produce different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.TokenTicker = "TOK1"
			},
			modifier2: func(tm *TokenMetadata) {
				tm.TokenTicker = "TOK2"
			},
			shouldBeEqual: false,
		},
		{
			name: "different decimals produce different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.Decimals = 8
			},
			modifier2: func(tm *TokenMetadata) {
				tm.Decimals = 6
			},
			shouldBeEqual: false,
		},
		{
			name: "different freezable flags produce different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.IsFreezable = true
			},
			modifier2: func(tm *TokenMetadata) {
				tm.IsFreezable = false
			},
			shouldBeEqual: false,
		},
		{
			name: "different max supply produces different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.MaxSupply = bytes.Repeat([]byte{0x01}, 16)
			},
			modifier2: func(tm *TokenMetadata) {
				tm.MaxSupply = bytes.Repeat([]byte{0x02}, 16)
			},
			shouldBeEqual: false,
		},
		{
			name: "different issuer public keys produce different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.IssuerPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
			},
			modifier2: func(tm *TokenMetadata) {
				tm.IssuerPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
			},
			shouldBeEqual: false,
		},
		{
			name: "different creation entity public keys produce different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.CreationEntityPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
			},
			modifier2: func(tm *TokenMetadata) {
				tm.CreationEntityPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
			},
			shouldBeEqual: false,
		},
		{
			name: "different extra metadata produces different hashes",
			modifier1: func(tm *TokenMetadata) {
				tm.ExtraMetadata = bytes.Repeat([]byte{0x01}, MaxExtraMetadataLength)
			},
			modifier2: func(tm *TokenMetadata) {
				tm.ExtraMetadata = bytes.Repeat([]byte{0x02}, MaxExtraMetadataLength)
			},
			shouldBeEqual: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var rng = rand.NewChaCha8([32]byte{})
			tm1 := createValidTokenMetadata(rng)
			if tc.modifier1 != nil {
				tc.modifier1(tm1)
			}

			rng.Seed([32]byte{}) // Reset rng
			tm2 := createValidTokenMetadata(rng)
			if tc.modifier2 != nil {
				tc.modifier2(tm2)
			}

			hash1, err1 := tm1.ComputeTokenIdentifier()
			require.NoError(t, err1)
			hash2, err2 := tm2.ComputeTokenIdentifier()
			require.NoError(t, err2)

			if tc.shouldBeEqual {
				assert.Equal(t, hash1, hash2)
			} else {
				assert.NotEqual(t, hash1, hash2)
			}
		})
	}
}
