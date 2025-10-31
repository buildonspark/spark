package tokens_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// queryAndVerifyTokenMetadata verifies token metadata using comprehensive checks (for metadata-focused tests)
func queryAndVerifyTokenMetadata(t *testing.T, config *wallet.TestWalletConfig, params sparkTokenCreationTestParams) {
	issuerPublicKey := params.issuerPrivateKey.Public()

	// Test 1: Query by issuer public key
	resp1, err := wallet.QueryTokenMetadata(t.Context(), config, nil, []keys.Public{issuerPublicKey})
	require.NoError(t, err, "failed to query token metadata by issuer public key")

	require.NotNil(t, resp1)
	require.Len(t, resp1.TokenMetadata, 1)

	metadata1 := resp1.TokenMetadata[0]
	verifyTokenMetadata(t, metadata1, params, "issuer public key query")

	// Test 2: Query by token identifier (using the one from the first query)
	tokenIdentifier := metadata1.TokenIdentifier
	resp2, err := wallet.QueryTokenMetadata(t.Context(), config, [][]byte{tokenIdentifier}, nil)
	require.NoError(t, err, "failed to query token metadata by token identifier")
	require.NotNil(t, resp2, "token metadata response should not be nil")
	require.Len(t, resp2.TokenMetadata, 1)

	metadata2 := resp2.TokenMetadata[0]
	verifyTokenMetadata(t, metadata2, params, "token identifier query")

	// Verify the token identifiers match
	require.Equal(t, metadata1.TokenIdentifier, metadata2.TokenIdentifier, "token identifier should be identical across queries")
}

func TestQueryTokenMetadataWithNoParams(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	_, err := wallet.QueryTokenMetadata(t.Context(), config, nil, nil)
	require.Error(t, err, "calling query token metadata with no params should return an error")
}

func TestQueryTokenMetadataL1Token(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	l1TokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: config.IdentityPrivateKey,
		name:             testTokenName,
		ticker:           testTokenTicker,
		maxSupply:        testTokenMaxSupply,
		expectedError:    false,
	}
	queryAndVerifyTokenMetadata(t, config, l1TokenParams)
}

func TestQueryTokenMetadataNativeSparkToken(t *testing.T) {
	nativeTokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: keys.GeneratePrivateKey(),
		name:             "Native Test Token",
		ticker:           "NATIVE",
		maxSupply:        5000000,
	}

	err := createNativeToken(t, nativeTokenParams)
	require.NoError(t, err, "failed to create native token")

	config := wallet.NewTestWalletConfigWithIdentityKey(t, nativeTokenParams.issuerPrivateKey)
	require.NoError(t, err, "failed to create wallet config")

	queryAndVerifyTokenMetadata(t, config, nativeTokenParams)
}

func TestQueryTokenMetadataMixedParams(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	nativeTokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: keys.GeneratePrivateKey(),
		name:             "Native Token",
		ticker:           "NATIV",
		maxSupply:        1000000,
	}
	err := createNativeToken(t, nativeTokenParams)
	require.NoError(t, err, "failed to create native token")
	nativeTokenIdentifier := verifyNativeToken(t, nativeTokenParams)

	l1TokenIssuerPubKey := config.IdentityPrivateKey.Public()

	// Test: Query for both tokens using mixed parameters in a single call
	mixedResp, err := wallet.QueryTokenMetadata(
		t.Context(),
		config,
		[][]byte{nativeTokenIdentifier},
		[]keys.Public{l1TokenIssuerPubKey},
	)
	require.NoError(t, err, "failed to query token metadata with mixed parameters")
	require.Len(t, mixedResp.TokenMetadata, 2)

	var foundNativeToken, foundL1Token bool
	l1TokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: config.IdentityPrivateKey,
		name:             testTokenName,
		ticker:           testTokenTicker,
		maxSupply:        testTokenMaxSupply,
	}

	for _, metadata := range mixedResp.TokenMetadata {
		if bytes.Equal(metadata.TokenIdentifier, nativeTokenIdentifier) {
			foundNativeToken = true
			verifyTokenMetadata(t, metadata, nativeTokenParams, "mixed query - native token")
		} else if issuerPubKey, err := keys.ParsePublicKey(metadata.IssuerPublicKey); err == nil && issuerPubKey.Equals(l1TokenIssuerPubKey) {
			foundL1Token = true
			verifyTokenMetadata(t, metadata, l1TokenParams, "mixed query - L1 token")
		}
	}

	require.True(t, foundNativeToken, "native token should be found in mixed query results")
	require.True(t, foundL1Token, "L1 token should be found in mixed query results")
}

func TestCoordinatedCreateNativeSparkTokenScenarios(t *testing.T) {
	fixedRandomKey := keys.GeneratePrivateKey()

	testCases := []struct {
		name              string
		firstTokenParams  *sparkTokenCreationTestParams
		secondTokenParams *sparkTokenCreationTestParams
	}{
		{
			name: "create second token with same issuer key should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: fixedRandomKey,
				name:             testTokenName,
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: fixedRandomKey,
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        testTokenMaxSupply + 1000,
				expectedError:    true,
			},
		},
		{
			name: "create two tokens with same metadata but different random keys should succeed",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             testTokenName,
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        testTokenMaxSupply,
			},
		},
		{
			name: "create two tokens with different metadata and different random keys should succeed",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             testTokenName,
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        testTokenMaxSupply + 1000,
			},
		},
		{
			name: "create token with name longer than 20 characters should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             "This Token Name Is Way Too Long For The System",
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with empty name should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             "",
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with empty ticker should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             testTokenName,
				ticker:           "",
				maxSupply:        testTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with ticker longer than 5 characters should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: keys.GeneratePrivateKey(),
				name:             testTokenName,
				ticker:           "TOOLONG",
				maxSupply:        testTokenMaxSupply,
				expectedError:    true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			firstTokenConfig := wallet.NewTestWalletConfigWithIdentityKey(t, tc.firstTokenParams.issuerPrivateKey)

			err := createNativeToken(t, *tc.firstTokenParams)
			if tc.firstTokenParams.expectedError {
				require.Error(t, err, "expected error but got none for first token creation")
				return
			}
			require.NoError(t, err, "unexpected error during first token creation")

			firstTokenIdentifier := verifyNativeToken(t, *tc.firstTokenParams)
			require.NotNil(t, firstTokenIdentifier, "first token should have been created successfully")

			if tc.secondTokenParams != nil {
				secondTokenConfig := wallet.NewTestWalletConfigWithIdentityKey(t, tc.secondTokenParams.issuerPrivateKey)

				err = testCoordinatedCreateNativeSparkTokenWithParams(t, secondTokenConfig, *tc.secondTokenParams)
				if tc.secondTokenParams.expectedError {
					require.Error(t, err, "expected error but got none for second token creation")
					stat, ok := status.FromError(err)
					require.True(t, ok, "expected error to be a gRPC status error")
					require.Equal(t, codes.AlreadyExists, stat.Code(), "expected gRPC status code to be AlreadyExists when token already created for issuer")
				} else {
					require.NoError(t, err, "unexpected error during second token creation")

					secondTokenIdentifier := verifyNativeToken(t, *tc.secondTokenParams)
					require.NotNil(t, secondTokenIdentifier, "second token should have been created successfully")

					verifyMultipleTokenIdentifiersQuery(t, firstTokenConfig, [][]byte{
						firstTokenIdentifier,
						secondTokenIdentifier,
					}, 2)
				}
			}
		})
	}
}

func TestCoordinatedNativeTokenMaxSupplyEnforcement(t *testing.T) {
	testCases := []struct {
		name                 string
		maxSupply            uint64
		mintAmounts          []uint64
		startExtraMintBefore bool
		expectedResults      []bool
	}{
		{
			name:            "mints should fail if exceeding max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{500, 600},
			expectedResults: []bool{true, false},
		},
		{
			name:            "mints should succeed if within max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{400, 500},
			expectedResults: []bool{true, true},
		},
		{
			name:            "mints should succeed if exactly matching max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{600, 400},
			expectedResults: []bool{true, true},
		},
		{
			name:            "mints should succeed if has unlimited max supply",
			maxSupply:       0,
			mintAmounts:     []uint64{1000000, 2000000},
			expectedResults: []bool{true, true},
		},
		{
			name:            "mints should fail if single mint exceeds max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{1001},
			expectedResults: []bool{false},
		},
		{
			name:                 "mints should still succeed if mint started before",
			maxSupply:            1000,
			mintAmounts:          []uint64{1000},
			expectedResults:      []bool{true},
			startExtraMintBefore: true,
		},
	}

	for _, tc := range testCases {
		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

		tokenPrivKey := keys.GeneratePrivateKey()
		err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
			issuerPrivateKey: tokenPrivKey,
			name:             "MaxTest",
			ticker:           "MAXT",
			maxSupply:        tc.maxSupply,
		})
		require.NoError(t, err, "failed to create native spark token with max supply %d", tc.maxSupply)

		for i, mintAmount := range tc.mintAmounts {
			expectedResult := tc.expectedResults[i]

			mintTransaction, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: tokenPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          1,
				OutputAmounts:       []uint64{mintAmount},
			})
			require.NoError(t, err, "failed to create mint transaction %d", i+1)

			if tc.startExtraMintBefore {
				mintTransaction.ClientCreatedTimestamp = timestamppb.New(time.Now().Add(-time.Second))
				_, _, err = wallet.StartTokenTransaction(
					t.Context(),
					config,
					mintTransaction,
					[]keys.Private{tokenPrivKey},
					TestValidityDurationSecs*time.Second,
					nil,
				)
				require.NoError(t, err, "failed to start mint transaction before")
			}

			_, err = wallet.BroadcastTokenTransfer(
				t.Context(), config, mintTransaction,
				[]keys.Private{tokenPrivKey},
			)

			if expectedResult {
				require.NoError(t, err, "mint %d of %d tokens should succeed (total so far: %d, max supply: %d)",
					i+1, mintAmount, sumUint64Slice(tc.mintAmounts[:i+1]), tc.maxSupply)
			} else {
				require.Error(t, err, "mint %d of %d tokens should fail (total would be: %d, max supply: %d)",
					i+1, mintAmount, sumUint64Slice(tc.mintAmounts[:i+1]), tc.maxSupply)
				require.ErrorContains(t, err, "max supply", "error should mention max supply")
			}
		}
	}
}
