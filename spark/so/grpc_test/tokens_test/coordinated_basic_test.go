package tokens_test

import (
	"math/big"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

func TestCoordinatedL1TokenMint(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			tokenPrivKey := config.IdentityPrivateKey

			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")
			require.Len(t, finalIssueTokenTransaction.TokenOutputs, 2, "expected 2 created outputs in mint transaction")

			userOneConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userOutput1PrivKey)
			userTwoConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userOutput2PrivKey)

			userOneBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userOneConfig,
				[]keys.Public{userOneConfig.IdentityPublicKey()},
				[]keys.Public{tokenPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user one token outputs")

			userTwoBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userTwoConfig,
				[]keys.Public{userTwoConfig.IdentityPublicKey()},
				[]keys.Public{tokenPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user two token outputs")

			require.Len(t, userOneBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user one")
			userOneAmount := bytesToBigInt(userOneBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(testIssueOutput1Amount), userOneAmount, "user one should have the first mint output amount")

			require.Len(t, userTwoBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user two")
			userTwoAmount := bytesToBigInt(userTwoBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(testIssueOutput2Amount), userTwoAmount, "user two should have the second mint output amount")
		})
	}
}

func TestCoordinatedL1TokenMintAndTransfer(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey})
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

			transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransactionTokenPb(t, config,
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
		})
	}
}

func TestCoordinatedTokenMintV3(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivKey := getRandomPrivateKey(t)
			config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
				IssuerPrivateKey: issuerPrivKey,
				Name:             testTokenName,
				Ticker:           testTokenTicker,
				MaxSupply:        testTokenMaxSupply,
			})
			require.NoError(t, err, "failed to create native spark token")

			issueTokenTransaction, userPrivKeys, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: issuerPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          2,
				OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
				Version:             TokenTransactionVersion3,
			})
			require.NoError(t, err, "failed to create test token issuance transaction")
			require.Len(t, userPrivKeys, 2)
			userOutput1PrivKey := userPrivKeys[0]
			userOutput2PrivKey := userPrivKeys[1]

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{issuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 issuance token transaction")
			require.Len(t, finalIssueTokenTransaction.TokenOutputs, 2, "expected 2 created outputs in V3 mint transaction")
			require.Equal(t, TokenTransactionVersion3, int(finalIssueTokenTransaction.Version), "final transaction should be V3")

			userOneConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userOutput1PrivKey)
			userTwoConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userOutput2PrivKey)

			userOneBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userOneConfig,
				[]keys.Public{userOneConfig.IdentityPublicKey()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user one token outputs")

			userTwoBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userTwoConfig,
				[]keys.Public{userTwoConfig.IdentityPublicKey()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user two token outputs")

			require.Len(t, userOneBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user one")
			userOneAmount := bytesToBigInt(userOneBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(testIssueOutput1Amount), userOneAmount,
				"user one should have correct token amount")

			require.Len(t, userTwoBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user two")
			userTwoAmount := bytesToBigInt(userTwoBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(testIssueOutput2Amount), userTwoAmount,
				"user two should have correct token amount")
		})
	}
}

func TestCoordinatedTokenTransferV3(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivKey := getRandomPrivateKey(t)
			config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
				IssuerPrivateKey: issuerPrivKey,
				Name:             testTokenName,
				Ticker:           testTokenTicker,
				MaxSupply:        testTokenMaxSupply,
			})
			require.NoError(t, err, "failed to create native spark token")

			issueTokenTransaction, userPrivKeys, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: issuerPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          2,
				OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
				Version:             TokenTransactionVersion3,
			})
			require.NoError(t, err, "failed to create test token issuance transaction")
			require.Len(t, userPrivKeys, 2)
			userOutput1PrivKey := userPrivKeys[0]
			userOutput2PrivKey := userPrivKeys[1]

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{issuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 issuance token transaction")

			finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
			require.NoError(t, err, "failed to hash final issuance token transaction")

			transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey:            issuerPrivKey.Public(),
				UseTokenIdentifier:             true,
				FinalIssueTokenTransactionHash: finalIssueTokenTransactionHash,
				Version:                        TokenTransactionVersion3,
			})
			require.NoError(t, err, "failed to create test token transfer transaction")

			require.Equal(t, TokenTransactionVersion3, int(transferTokenTransaction.Version), "expected V3 version")

			transferTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 transfer token transaction")

			require.Equal(t, TokenTransactionVersion3, int(transferTokenTransactionResponse.Version), "final transfer transaction should be V3")
			require.Len(t, transferTokenTransactionResponse.TokenOutputs, 1, "expected 1 created output in V3 transfer transaction")

			userThreeConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userOutput3PrivKey)
			userThreeBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userThreeConfig,
				[]keys.Public{userThreeConfig.IdentityPublicKey()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user three token outputs")

			require.Len(t, userThreeBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user three")
			userThreeAmount := bytesToBigInt(userThreeBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(testTransferOutput1Amount), userThreeAmount,
				"user three should have correct token amount from V3 transfer")
		})
	}
}
