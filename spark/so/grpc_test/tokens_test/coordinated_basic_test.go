package tokens_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// TestCoordinatedTokenTransferWithMultipleTokenTypes tests transferring multiple token types in a single transaction
func TestCoordinatedTokenTransferWithMultipleTokenTypes(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			// Create two different native spark tokens
			token1IssuerPrivKey := getRandomPrivateKey(t)
			token2IssuerPrivKey := getRandomPrivateKey(t)

			config1 := wallet.NewTestWalletConfigWithIdentityKey(t, token1IssuerPrivKey)
			config2 := wallet.NewTestWalletConfigWithIdentityKey(t, token2IssuerPrivKey)

			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config1, createNativeSparkTokenParams{
				IssuerPrivateKey: token1IssuerPrivKey,
				Name:             "Token A",
				Ticker:           "TKA",
				MaxSupply:        1000000,
			})
			require.NoError(t, err, "failed to create token A")

			err = testCoordinatedCreateNativeSparkTokenWithParams(t, config2, createNativeSparkTokenParams{
				IssuerPrivateKey: token2IssuerPrivKey,
				Name:             "Token B",
				Ticker:           "TKB",
				MaxSupply:        2000000,
			})
			require.NoError(t, err, "failed to create token B")

			token1Identifier, err := getTokenIdentifierFromMetadata(t.Context(), config1, token1IssuerPrivKey.Public())
			require.NoError(t, err, "failed to get token A identifier")

			token2Identifier, err := getTokenIdentifierFromMetadata(t.Context(), config2, token2IssuerPrivKey.Public())
			require.NoError(t, err, "failed to get token B identifier")

			// Mint token A to a user
			userPrivKey := keys.GeneratePrivateKey()
			mintToken1Tx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config1, tokenTransactionParams{
				TokenIdentityPubKey: token1IssuerPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          1,
				OutputAmounts:       []uint64{1000},
			})
			require.NoError(t, err, "failed to create mint transaction for token A")
			mintToken1Tx.TokenOutputs[0].OwnerPublicKey = userPrivKey.Public().Serialize()

			finalMintToken1, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config1, mintToken1Tx,
				[]keys.Private{token1IssuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast mint transaction for token A")

			mintToken1Hash, err := utils.HashTokenTransaction(finalMintToken1, false)
			require.NoError(t, err, "failed to hash mint transaction for token A")

			// Mint token B to the same user
			mintToken2Tx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config2, tokenTransactionParams{
				TokenIdentityPubKey: token2IssuerPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          1,
				OutputAmounts:       []uint64{2000},
			})
			require.NoError(t, err, "failed to create mint transaction for token B")
			mintToken2Tx.TokenOutputs[0].OwnerPublicKey = userPrivKey.Public().Serialize()

			finalMintToken2, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config2, mintToken2Tx,
				[]keys.Private{token2IssuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast mint transaction for token B")

			mintToken2Hash, err := utils.HashTokenTransaction(finalMintToken2, false)
			require.NoError(t, err, "failed to hash mint transaction for token B")

			// Create a transfer transaction that spends both token types and creates outputs in both token types
			recipient1PrivKey := keys.GeneratePrivateKey()
			recipient2PrivKey := keys.GeneratePrivateKey()

			multiTokenTransferTx := &tokenpb.TokenTransaction{
				Version: TokenTransactionVersion2,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: mintToken1Hash,
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: mintToken2Hash,
								PrevTokenTransactionVout: 0,
							},
						},
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						OwnerPublicKey:  recipient1PrivKey.Public().Serialize(),
						TokenIdentifier: token1Identifier,
						TokenAmount:     int64ToUint128Bytes(0, 600),
					},
					{
						OwnerPublicKey:  recipient2PrivKey.Public().Serialize(),
						TokenIdentifier: token1Identifier,
						TokenAmount:     int64ToUint128Bytes(0, 400),
					},
					{
						OwnerPublicKey:  recipient1PrivKey.Public().Serialize(),
						TokenIdentifier: token2Identifier,
						TokenAmount:     int64ToUint128Bytes(0, 1200),
					},
					{
						OwnerPublicKey:  recipient2PrivKey.Public().Serialize(),
						TokenIdentifier: token2Identifier,
						TokenAmount:     int64ToUint128Bytes(0, 800),
					},
				},
				Network:                         config.ProtoNetwork(),
				SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
				ClientCreatedTimestamp:          timestamppb.New(time.Now()),
			}

			finalTransferTx, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, multiTokenTransferTx,
				[]keys.Private{userPrivKey, userPrivKey},
			)
			require.NoError(t, err, "failed to broadcast multi-token transfer transaction")

			require.Len(t, finalTransferTx.TokenOutputs, 4, "expected 4 outputs in multi-token transfer")

			// Verify recipient 1 received correct amounts of both tokens
			recipient1Config := wallet.NewTestWalletConfigWithIdentityKey(t, recipient1PrivKey)
			recipient1Token1Outputs, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				recipient1Config,
				[]keys.Public{recipient1PrivKey.Public()},
				[]keys.Public{token1IssuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query recipient 1 token A outputs")
			require.Len(t, recipient1Token1Outputs.OutputsWithPreviousTransactionData, 1, "expected 1 token A output for recipient 1")
			recipient1Token1Amount := bytesToBigInt(recipient1Token1Outputs.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(600), recipient1Token1Amount, "recipient 1 should have 600 token A")

			recipient1Token2Outputs, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				recipient1Config,
				[]keys.Public{recipient1PrivKey.Public()},
				[]keys.Public{token2IssuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query recipient 1 token B outputs")
			require.Len(t, recipient1Token2Outputs.OutputsWithPreviousTransactionData, 1, "expected 1 token B output for recipient 1")
			recipient1Token2Amount := bytesToBigInt(recipient1Token2Outputs.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(1200), recipient1Token2Amount, "recipient 1 should have 1200 token B")

			// Verify recipient 2 received correct amounts of both tokens
			recipient2Config := wallet.NewTestWalletConfigWithIdentityKey(t, recipient2PrivKey)
			recipient2Token1Outputs, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				recipient2Config,
				[]keys.Public{recipient2PrivKey.Public()},
				[]keys.Public{token1IssuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query recipient 2 token A outputs")
			require.Len(t, recipient2Token1Outputs.OutputsWithPreviousTransactionData, 1, "expected 1 token A output for recipient 2")
			recipient2Token1Amount := bytesToBigInt(recipient2Token1Outputs.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(400), recipient2Token1Amount, "recipient 2 should have 400 token A")

			recipient2Token2Outputs, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				recipient2Config,
				[]keys.Public{recipient2PrivKey.Public()},
				[]keys.Public{token2IssuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query recipient 2 token B outputs")
			require.Len(t, recipient2Token2Outputs.OutputsWithPreviousTransactionData, 1, "expected 1 token B output for recipient 2")
			recipient2Token2Amount := bytesToBigInt(recipient2Token2Outputs.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(800), recipient2Token2Amount, "recipient 2 should have 800 token B")

			// Verify token conservation: inputs of each type equal outputs of each type
			// Token A: 1000 (input) = 600 + 400 (outputs) ✓
			// Token B: 2000 (input) = 1200 + 800 (outputs) ✓
		})
	}
}
