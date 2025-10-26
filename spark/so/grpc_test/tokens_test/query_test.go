package tokens_test

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

// encodeSparkAddress is a helper function to encode a public key as a spark address for testing
func encodeSparkAddress(pubKey keys.Public, network common.Network) string {
	address, err := common.EncodeSparkAddress(pubKey.Serialize(), network, nil)
	if err != nil {
		panic(err)
	}
	return address
}

// TestCoordinatedTokenMintAndTransferExpectedOutputAndTxRetrieval tests the full coordinated flow with mint and transfer
// This test also verifies that upon success that the expected outputs and transactions are retrievable.
func TestCoordinatedTokenMintAndTransferExpectedOutputAndTxRetrieval(t *testing.T) {
	issuerPrivKey := getRandomPrivateKey(t)
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

	err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
		issuerPrivateKey: issuerPrivKey,
		name:             testTokenName,
		ticker:           testTokenTicker,
		maxSupply:        testTokenMaxSupply,
	})
	require.NoError(t, err, "failed to create native spark token")

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		if output.GetWithdrawBondSats() != withdrawalBondSatsInConfig {
			t.Errorf("output %d: expected withdrawal bond sats 10000, got %d", i, output.GetWithdrawBondSats())
		}
		if output.GetWithdrawRelativeBlockLocktime() != uint64(withdrawalRelativeBlockLocktimeInConfig) {
			t.Errorf("output %d: expected withdrawal relative block locktime 1000, got %d", i, output.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransactionTokenPb(t,
		config,
		finalIssueTokenTransactionHash,
		tokenPrivKey.Public(),
	)
	require.NoError(t, err, "failed to create test token transfer transaction")
	userOutput3PubKeyBytes := userOutput3PrivKey.Public().Serialize()

	transferTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, transferTokenTransaction, []keys.Private{userOutput1PrivKey, userOutput2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	require.Len(t, transferTokenTransactionResponse.TokenOutputs, 1, "expected 1 created output in transfer transaction")
	transferAmount := new(big.Int).SetBytes(transferTokenTransactionResponse.TokenOutputs[0].TokenAmount)
	expectedTransferAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, testTransferOutput1Amount))
	require.Equal(t, 0, transferAmount.Cmp(expectedTransferAmount), "transfer amount does not match expected")
	require.Equal(t, userOutput3PubKeyBytes, transferTokenTransactionResponse.TokenOutputs[0].OwnerPublicKey, "transfer created output owner public key does not match expected")

	tokenOutputsResponse, err := wallet.QueryTokenOutputsV2(
		t.Context(),
		config,
		[]keys.Public{userOutput3PrivKey.Public()},
		[]keys.Public{tokenPrivKey.Public()},
	)
	require.NoError(t, err, "failed to get owned token outputs")
	require.Len(t, tokenOutputsResponse.OutputsWithPreviousTransactionData, 1, "expected 1 output after transfer transaction")
	require.Equal(t, expectedTransferAmount, new(big.Int).SetBytes(tokenOutputsResponse.OutputsWithPreviousTransactionData[0].Output.TokenAmount), "expected correct amount after transfer transaction")

	// Test QueryTokenTransactionsNative with pagination - first page
	page1Params := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  []keys.Public{tokenPrivKey.Public()},
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: nil,
		Offset:            0,
		Limit:             1,
	}
	tokenTransactionsPage1, err := wallet.QueryTokenTransactionsV2(
		t.Context(),
		config,
		page1Params,
	)
	require.NoError(t, err, "failed to query token transactions page 1")

	require.Len(t, tokenTransactionsPage1.TokenTransactionsWithStatus, 1, "expected 1 token transaction in page 1")
	require.Equal(t, int64(1), tokenTransactionsPage1.Offset, "expected next offset 1 for page 1")

	transferTx := tokenTransactionsPage1.TokenTransactionsWithStatus[0].TokenTransaction
	require.NotNil(t, transferTx.GetTransferInput(), "first transaction should be a transfer transaction")

	// Test QueryTokenTransactionsNative with pagination - second page
	page2Params := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  []keys.Public{tokenPrivKey.Public()},
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: nil,
		Offset:            tokenTransactionsPage1.Offset,
		Limit:             1,
	}
	tokenTransactionsPage2, err := wallet.QueryTokenTransactionsV2(t.Context(), config, page2Params)
	require.NoError(t, err, "failed to query token transactions page 2")

	require.Len(t, tokenTransactionsPage2.TokenTransactionsWithStatus, 1, "expected 1 token transaction in page 2")
	require.Equal(t, int64(2), tokenTransactionsPage2.Offset, "expected next offset 2 for page 2")

	mintTx := tokenTransactionsPage2.TokenTransactionsWithStatus[0].TokenTransaction
	require.NotNil(t, mintTx.GetMintInput(), "second transaction should be a mint transaction")
	require.Equal(t, tokenPrivKey.Public().Serialize(), mintTx.GetMintInput().GetIssuerPublicKey(), "mint transaction issuer public key does not match expected")

	// Test QueryTokenTransactionsNative with pagination - third page (should be empty)
	page3Params := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  []keys.Public{tokenPrivKey.Public()},
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: nil,
		Offset:            tokenTransactionsPage2.Offset,
		Limit:             1,
	}
	tokenTransactionsPage3, err := wallet.QueryTokenTransactionsV2(t.Context(), config, page3Params)
	require.NoError(t, err, "failed to query token transactions page 3")

	require.Empty(t, tokenTransactionsPage3.TokenTransactionsWithStatus, "expected 0 token transactions in page 3")
	require.Equal(t, int64(-1), tokenTransactionsPage3.Offset, "expected next offset -1 for page 3")

	// Validate transfer transaction details
	require.Len(t, transferTx.TokenOutputs, 1, "expected 1 created output in transfer transaction")
	transferAmount = new(big.Int).SetBytes(transferTx.TokenOutputs[0].TokenAmount)
	require.Equal(t, 0, transferAmount.Cmp(expectedTransferAmount), "transfer amount does not match expected")
	require.Equal(t, userOutput3PubKeyBytes, transferTx.TokenOutputs[0].OwnerPublicKey, "transfer created output owner public key does not match expected")

	// Validate mint transaction details
	require.Len(t, mintTx.TokenOutputs, 2, "expected 2 created outputs in mint transaction")
	userOutput1Pubkey := userOutput1PrivKey.Public().Serialize()
	userOutput2Pubkey := userOutput2PrivKey.Public().Serialize()

	if bytes.Equal(mintTx.TokenOutputs[0].OwnerPublicKey, userOutput1Pubkey) {
		require.Equal(t, mintTx.TokenOutputs[1].OwnerPublicKey, userOutput2Pubkey)
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[0].TokenAmount), uint64ToBigInt(testIssueOutput1Amount))
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[1].TokenAmount), uint64ToBigInt(testIssueOutput2Amount))
	} else if bytes.Equal(mintTx.TokenOutputs[0].OwnerPublicKey, userOutput2Pubkey) {
		require.Equal(t, mintTx.TokenOutputs[1].OwnerPublicKey, userOutput1Pubkey)
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[0].TokenAmount), uint64ToBigInt(testIssueOutput2Amount))
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[1].TokenAmount), uint64ToBigInt(testIssueOutput1Amount))
	} else {
		t.Fatalf("mint transaction output keys (%x, %x) do not match expected (%x, %x)",
			mintTx.TokenOutputs[0].OwnerPublicKey,
			mintTx.TokenOutputs[1].OwnerPublicKey,
			userOutput1Pubkey,
			userOutput2Pubkey,
		)
	}
}

// TestQueryTokenTransactionsWithMultipleFilters tests QueryTokenTransactions with various filter combinations
func TestQueryTokenTransactionsWithMultipleFilters(t *testing.T) {
	issuerPrivKey := getRandomPrivateKey(t)
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

	err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
		issuerPrivateKey: issuerPrivKey,
		name:             "Filter Test Token",
		ticker:           "FLTR",
		maxSupply:        1000000,
	})
	require.NoError(t, err, "failed to create native spark token")

	// Create first mint transaction with 2 outputs
	mintTransaction1, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create first mint transaction")

	finalMintTx1, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTransaction1, []keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast first mint transaction")

	mintTxHash1, err := utils.HashTokenTransaction(finalMintTx1, false)
	require.NoError(t, err, "failed to hash first mint transaction")

	// Create second mint transaction with 2 outputs
	mintTransaction2, userOutput3PrivKey, userOutput4PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create second mint transaction")

	finalMintTx2, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTransaction2, []keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast second mint transaction")

	mintTxHash2, err := utils.HashTokenTransaction(finalMintTx2, false)
	require.NoError(t, err, "failed to hash second mint transaction")

	// Create a transfer transaction
	transferTx, userOutput5PrivKey, err := createTestTokenTransferTransactionTokenPb(t, config, mintTxHash1, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create transfer transaction")

	finalTransferTx, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, transferTx,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast transfer transaction")

	transferTxHash, err := utils.HashTokenTransaction(finalTransferTx, false)
	require.NoError(t, err, "failed to hash transfer transaction")

	tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to get token identifier")

	// Create a SECOND token with different identifier to test token identifier filtering
	issuer2PrivKey := getRandomPrivateKey(t)
	config2 := wallet.NewTestWalletConfigWithIdentityKey(t, issuer2PrivKey)

	err = testCoordinatedCreateNativeSparkTokenWithParams(t, config2, sparkTokenCreationTestParams{
		issuerPrivateKey: issuer2PrivKey,
		name:             "Second Filter Token",
		ticker:           "FLT2",
		maxSupply:        500000,
	})
	require.NoError(t, err, "failed to create second native spark token")

	// Create mint transaction for second token
	mintTransaction3, userOutput6PrivKey, _, err := createTestTokenMintTransactionTokenPb(t, config2, issuer2PrivKey.Public())
	require.NoError(t, err, "failed to create third mint transaction")

	finalMintTx3, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config2, mintTransaction3, []keys.Private{issuer2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast third mint transaction")

	mintTxHash3, err := utils.HashTokenTransaction(finalMintTx3, false)
	require.NoError(t, err, "failed to hash third mint transaction")

	tokenIdentifier2, err := getTokenIdentifierFromMetadata(t.Context(), config2, issuer2PrivKey.Public())
	require.NoError(t, err, "failed to get second token identifier")

	// Collect output IDs
	mintTx1Output1ID := *finalMintTx1.TokenOutputs[0].Id
	mintTx1Output2ID := *finalMintTx1.TokenOutputs[1].Id
	mintTx2Output1ID := *finalMintTx2.TokenOutputs[0].Id
	mintTx3Output1ID := *finalMintTx3.TokenOutputs[0].Id
	transferTxOutputID := *finalTransferTx.TokenOutputs[0].Id

	testCases := []struct {
		name                  string
		params                wallet.QueryTokenTransactionsParams
		expectedTxCount       int
		shouldContainTxHashes [][]byte
	}{
		{
			name: "filter by issuer public key only",
			params: wallet.QueryTokenTransactionsParams{
				IssuerPublicKeys: []keys.Public{issuerPrivKey.Public()},
				Limit:            10,
			},
			expectedTxCount:       3,
			shouldContainTxHashes: [][]byte{mintTxHash1, mintTxHash2, transferTxHash},
		},
		{
			name: "filter by owner public key - user output 1",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys: []keys.Public{userOutput1PrivKey.Public()},
				Limit:           10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by owner public key - user output 5 (transfer recipient)",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys: []keys.Public{userOutput5PrivKey.Public()},
				Limit:           10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{transferTxHash},
		},
		{
			name: "filter by token identifier - first token",
			params: wallet.QueryTokenTransactionsParams{
				TokenIdentifiers: [][]byte{tokenIdentifier},
				Limit:            10,
			},
			expectedTxCount:       3,
			shouldContainTxHashes: [][]byte{mintTxHash1, mintTxHash2, transferTxHash},
		},
		{
			name: "filter by token identifier - second token",
			params: wallet.QueryTokenTransactionsParams{
				TokenIdentifiers: [][]byte{tokenIdentifier2},
				Limit:            10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash3},
		},
		{
			name: "filter by multiple token identifiers",
			params: wallet.QueryTokenTransactionsParams{
				TokenIdentifiers: [][]byte{tokenIdentifier, tokenIdentifier2},
				Limit:            10,
			},
			expectedTxCount:       4,
			shouldContainTxHashes: [][]byte{mintTxHash1, mintTxHash2, transferTxHash, mintTxHash3},
		},
		{
			name: "filter by output ID - single output",
			params: wallet.QueryTokenTransactionsParams{
				OutputIDs: []string{mintTx1Output1ID},
				Limit:     10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by output ID - multiple outputs from same transaction",
			params: wallet.QueryTokenTransactionsParams{
				OutputIDs: []string{mintTx1Output1ID, mintTx1Output2ID},
				Limit:     10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by output ID - outputs from different transactions",
			params: wallet.QueryTokenTransactionsParams{
				OutputIDs: []string{mintTx2Output1ID, mintTx3Output1ID},
				Limit:     10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash2, mintTxHash3},
		},
		{
			name: "filter by transaction hash - single",
			params: wallet.QueryTokenTransactionsParams{
				TransactionHashes: [][]byte{mintTxHash1},
				Limit:             10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash1},
		},
		{
			name: "filter by transaction hash - multiple",
			params: wallet.QueryTokenTransactionsParams{
				TransactionHashes: [][]byte{mintTxHash1, transferTxHash},
				Limit:             10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by owner public key AND issuer public key",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys:  []keys.Public{userOutput1PrivKey.Public()},
				IssuerPublicKeys: []keys.Public{issuerPrivKey.Public()},
				Limit:            10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by owner public key AND token identifier - first token",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys:  []keys.Public{userOutput5PrivKey.Public()},
				TokenIdentifiers: [][]byte{tokenIdentifier},
				Limit:            10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{transferTxHash},
		},
		{
			name: "filter by owner public key AND token identifier - second token",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys:  []keys.Public{userOutput6PrivKey.Public()},
				TokenIdentifiers: [][]byte{tokenIdentifier2},
				Limit:            10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash3},
		},
		{
			name: "filter by owner AND token identifier - mismatched token",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys:  []keys.Public{userOutput6PrivKey.Public()},
				TokenIdentifiers: [][]byte{tokenIdentifier},
				Limit:            10,
			},
			expectedTxCount:       0,
			shouldContainTxHashes: [][]byte{},
		},
		{
			name: "filter by output ID AND transaction hash",
			params: wallet.QueryTokenTransactionsParams{
				OutputIDs:         []string{mintTx1Output1ID},
				TransactionHashes: [][]byte{mintTxHash1},
				Limit:             10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash1},
		},
		{
			name: "filter by output ID AND transaction hash - should match transfer too",
			params: wallet.QueryTokenTransactionsParams{
				OutputIDs:         []string{mintTx1Output1ID},
				TransactionHashes: [][]byte{transferTxHash},
				Limit:             10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{transferTxHash},
		},
		{
			name: "filter by owner, issuer, and token identifier - all matching",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys:  []keys.Public{userOutput1PrivKey.Public()},
				IssuerPublicKeys: []keys.Public{issuerPrivKey.Public()},
				TokenIdentifiers: [][]byte{tokenIdentifier},
				Limit:            10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by multiple owner public keys - same transaction",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys: []keys.Public{userOutput3PrivKey.Public(), userOutput4PrivKey.Public()},
				Limit:           10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash2},
		},
		{
			name: "filter by multiple owner public keys - mixed single and multiple transactions",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys: []keys.Public{
					userOutput1PrivKey.Public(),
					userOutput2PrivKey.Public(),
					userOutput3PrivKey.Public(),
				},
				Limit: 10,
			},
			expectedTxCount:       3,
			shouldContainTxHashes: [][]byte{mintTxHash1, mintTxHash2, transferTxHash},
		},
		{
			name: "filter by output from transfer transaction",
			params: wallet.QueryTokenTransactionsParams{
				OutputIDs: []string{transferTxOutputID},
				Limit:     10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{transferTxHash},
		},
		{
			name: "filter by output from second token",
			params: wallet.QueryTokenTransactionsParams{
				OutputIDs: []string{mintTx3Output1ID},
				Limit:     10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash3},
		},
		{
			name: "filter by non-existent transaction hash",
			params: wallet.QueryTokenTransactionsParams{
				TransactionHashes: [][]byte{make([]byte, 32)},
				Limit:             10,
			},
			expectedTxCount:       0,
			shouldContainTxHashes: [][]byte{},
		},
		{
			name: "filter by non-existent owner public key",
			params: wallet.QueryTokenTransactionsParams{
				OwnerPublicKeys: []keys.Public{keys.GeneratePrivateKey().Public()},
				Limit:           10,
			},
			expectedTxCount:       0,
			shouldContainTxHashes: [][]byte{},
		},
		{
			name: "filter by spark address - user output 1",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses: []string{encodeSparkAddress(userOutput1PrivKey.Public(), config.Network)},
				Limit:          10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by spark address - user output 5 (transfer recipient)",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses: []string{encodeSparkAddress(userOutput5PrivKey.Public(), config.Network)},
				Limit:          10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{transferTxHash},
		},
		{
			name: "filter by spark address AND issuer public key",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses:   []string{encodeSparkAddress(userOutput1PrivKey.Public(), config.Network)},
				IssuerPublicKeys: []keys.Public{issuerPrivKey.Public()},
				Limit:            10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by spark address AND token identifier - first token",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses:   []string{encodeSparkAddress(userOutput5PrivKey.Public(), config.Network)},
				TokenIdentifiers: [][]byte{tokenIdentifier},
				Limit:            10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{transferTxHash},
		},
		{
			name: "filter by spark address AND token identifier - second token",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses:   []string{encodeSparkAddress(userOutput6PrivKey.Public(), config.Network)},
				TokenIdentifiers: [][]byte{tokenIdentifier2},
				Limit:            10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash3},
		},
		{
			name: "filter by spark address AND token identifier - mismatched token",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses:   []string{encodeSparkAddress(userOutput6PrivKey.Public(), config.Network)},
				TokenIdentifiers: [][]byte{tokenIdentifier},
				Limit:            10,
			},
			expectedTxCount:       0,
			shouldContainTxHashes: [][]byte{},
		},
		{
			name: "filter by spark address, issuer, and token identifier - all matching",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses:   []string{encodeSparkAddress(userOutput1PrivKey.Public(), config.Network)},
				IssuerPublicKeys: []keys.Public{issuerPrivKey.Public()},
				TokenIdentifiers: [][]byte{tokenIdentifier},
				Limit:            10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
		{
			name: "filter by multiple spark addresses - same transaction",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses: []string{
					encodeSparkAddress(userOutput3PrivKey.Public(), config.Network),
					encodeSparkAddress(userOutput4PrivKey.Public(), config.Network),
				},
				Limit: 10,
			},
			expectedTxCount:       1,
			shouldContainTxHashes: [][]byte{mintTxHash2},
		},
		{
			name: "filter by multiple spark addresses - mixed single and multiple transactions",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses: []string{
					encodeSparkAddress(userOutput1PrivKey.Public(), config.Network),
					encodeSparkAddress(userOutput2PrivKey.Public(), config.Network),
					encodeSparkAddress(userOutput3PrivKey.Public(), config.Network),
				},
				Limit: 10,
			},
			expectedTxCount:       3,
			shouldContainTxHashes: [][]byte{mintTxHash1, mintTxHash2, transferTxHash},
		},
		{
			name: "filter by non-existent spark address",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses: []string{encodeSparkAddress(keys.GeneratePrivateKey().Public(), config.Network)},
				Limit:          10,
			},
			expectedTxCount:       0,
			shouldContainTxHashes: [][]byte{},
		},
		{
			name: "filter by mixed spark addresses and owner public keys",
			params: wallet.QueryTokenTransactionsParams{
				SparkAddresses:  []string{encodeSparkAddress(userOutput1PrivKey.Public(), config.Network)},
				OwnerPublicKeys: []keys.Public{userOutput2PrivKey.Public()},
				Limit:           10,
			},
			expectedTxCount:       2,
			shouldContainTxHashes: [][]byte{mintTxHash1, transferTxHash},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := wallet.QueryTokenTransactionsV2(
				t.Context(),
				config,
				tc.params,
			)
			require.NoError(t, err, "failed to query token transactions")

			require.Len(t, result.TokenTransactionsWithStatus, tc.expectedTxCount,
				"expected %d transactions but got %d", tc.expectedTxCount, len(result.TokenTransactionsWithStatus))

			foundHashes := make(map[string]bool)
			for _, txWithStatus := range result.TokenTransactionsWithStatus {
				foundHashes[string(txWithStatus.TokenTransactionHash)] = true
			}

			for _, expectedHash := range tc.shouldContainTxHashes {
				require.True(t, foundHashes[string(expectedHash)],
					"expected to find transaction hash %x in results", expectedHash)
			}
		})
	}
}

// TestQueryTokenOutputsWithStartTransaction verifies that when a transfer
// transaction expires without being finalized, the spent outputs are returned again by
// QueryTokenOutputsV2.
func TestQueryTokenOutputsWithStartTransaction(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			issuerPrivKey := config.IdentityPrivateKey
			mintTx, owner1PrivKey, owner2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
			require.NoError(t, err, "failed to create mint transaction")

			finalTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, mintTx, []keys.Private{issuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast mint transaction")

			mintTxHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
			require.NoError(t, err, "failed to hash mint transaction")

			transferTx, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey:            issuerPrivKey.Public(),
				FinalIssueTokenTransactionHash: mintTxHash,
				NumOutputs:                     1,
				OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
			})
			require.NoError(t, err, "failed to create transfer transaction")

			_, _, err = wallet.StartTokenTransactionCoordinated(t.Context(), config, transferTx, []keys.Private{owner1PrivKey, owner2PrivKey}, 1*time.Second, nil)
			require.NoError(t, err, "failed to start transfer transaction")

			outputsResp, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				config,
				[]keys.Public{owner1PrivKey.Public()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query token outputs")

			require.Len(t, outputsResp.OutputsWithPreviousTransactionData, 1, "expected the spent output to be returned after transaction expiry")
			require.Equal(t, mintTxHash, outputsResp.OutputsWithPreviousTransactionData[0].PreviousTransactionHash, "expected the same previous transaction hash")
		})
	}
}

// TestQueryTokenTransactionsOrdering tests that QueryTokenTransactions returns results in the correct order
func TestQueryTokenTransactionsOrdering(t *testing.T) {
	issuerPrivKey := getRandomPrivateKey(t)
	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

	err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
		issuerPrivateKey: issuerPrivKey,
		name:             "Order Test Token",
		ticker:           "ORD",
		maxSupply:        1000000,
	})
	require.NoError(t, err, "failed to create native spark token")

	var transactionHashes [][]byte

	mintTx1, _, _, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create first mint transaction")

	finalMintTx1, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTx1, []keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast first mint transaction")

	mintTxHash1, err := utils.HashTokenTransaction(finalMintTx1, false)
	require.NoError(t, err, "failed to hash first mint transaction")
	transactionHashes = append(transactionHashes, mintTxHash1)

	time.Sleep(100 * time.Millisecond)

	mintTx2, _, _, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create second mint transaction")

	finalMintTx2, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTx2, []keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast second mint transaction")

	mintTxHash2, err := utils.HashTokenTransaction(finalMintTx2, false)
	require.NoError(t, err, "failed to hash second mint transaction")
	transactionHashes = append(transactionHashes, mintTxHash2)

	time.Sleep(100 * time.Millisecond)

	mintTx3, _, _, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create third mint transaction")

	finalMintTx3, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTx3, []keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast third mint transaction")

	mintTxHash3, err := utils.HashTokenTransaction(finalMintTx3, false)
	require.NoError(t, err, "failed to hash third mint transaction")
	transactionHashes = append(transactionHashes, mintTxHash3)

	t.Run("ascending order", func(t *testing.T) {
		result, err := wallet.QueryTokenTransactionsV2(
			t.Context(),
			config,
			wallet.QueryTokenTransactionsParams{
				IssuerPublicKeys: []keys.Public{issuerPrivKey.Public()},
				Order:            sparkpb.Order_ASCENDING,
				Limit:            10,
			},
		)
		require.NoError(t, err, "failed to query token transactions with ascending order")
		require.Len(t, result.TokenTransactionsWithStatus, 3, "expected 3 transactions")

		require.Equal(t, transactionHashes[0], result.TokenTransactionsWithStatus[0].TokenTransactionHash,
			"first transaction should be mintTxHash1")
		require.Equal(t, transactionHashes[1], result.TokenTransactionsWithStatus[1].TokenTransactionHash,
			"second transaction should be mintTxHash2")
		require.Equal(t, transactionHashes[2], result.TokenTransactionsWithStatus[2].TokenTransactionHash,
			"third transaction should be mintTxHash3")
	})

	t.Run("descending order", func(t *testing.T) {
		result, err := wallet.QueryTokenTransactionsV2(
			t.Context(),
			config,
			wallet.QueryTokenTransactionsParams{
				IssuerPublicKeys: []keys.Public{issuerPrivKey.Public()},
				Order:            sparkpb.Order_DESCENDING,
				Limit:            10,
			},
		)
		require.NoError(t, err, "failed to query token transactions with descending order")
		require.Len(t, result.TokenTransactionsWithStatus, 3, "expected 3 transactions")

		require.Equal(t, transactionHashes[2], result.TokenTransactionsWithStatus[0].TokenTransactionHash,
			"first transaction should be mintTxHash3")
		require.Equal(t, transactionHashes[1], result.TokenTransactionsWithStatus[1].TokenTransactionHash,
			"second transaction should be mintTxHash2")
		require.Equal(t, transactionHashes[0], result.TokenTransactionsWithStatus[2].TokenTransactionHash,
			"third transaction should be mintTxHash1")
	})
}
