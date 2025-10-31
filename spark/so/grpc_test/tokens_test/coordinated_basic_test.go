package tokens_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
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

			finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")
			require.Len(t, finalIssueTokenTransaction.TokenOutputs, 2, "expected 2 created outputs in mint transaction")

			verifyTokenBalance(t, userOutput1PrivKey, tokenPrivKey.Public(), testIssueOutput1Amount, "user one")
			verifyTokenBalance(t, userOutput2PrivKey, tokenPrivKey.Public(), testIssueOutput2Amount, "user two")
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

			finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey})
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
			transferTokenTransactionResponse, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, transferTokenTransaction, []keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast transfer token transaction")

			require.Len(t, transferTokenTransactionResponse.TokenOutputs, 1, "expected 1 created output in transfer transaction")
			transferAmount := new(big.Int).SetBytes(transferTokenTransactionResponse.TokenOutputs[0].TokenAmount)
			expectedTransferAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, testTransferOutput1Amount))
			assert.Equal(t, expectedTransferAmount, transferAmount)
			assert.Equal(t, userOutput3PrivKey.Public().Serialize(), transferTokenTransactionResponse.TokenOutputs[0].OwnerPublicKey, "transfer created output owner public key does not match expected")
		})
	}
}

func TestCoordinatedTokenMintV3(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivKey := keys.GeneratePrivateKey()
			config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
				issuerPrivateKey: issuerPrivKey,
				name:             testTokenName,
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
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

			finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{issuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 issuance token transaction")
			require.Len(t, finalIssueTokenTransaction.TokenOutputs, 2, "expected 2 created outputs in V3 mint transaction")
			require.Equal(t, TokenTransactionVersion3, int(finalIssueTokenTransaction.Version), "final transaction should be V3")

			verifyTokenBalance(t, userOutput1PrivKey, issuerPrivKey.Public(), testIssueOutput1Amount, "user one")
			verifyTokenBalance(t, userOutput2PrivKey, issuerPrivKey.Public(), testIssueOutput2Amount, "user two")
		})
	}
}

func TestCoordinatedTokenTransferV3(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivKey := keys.GeneratePrivateKey()
			config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
				issuerPrivateKey: issuerPrivKey,
				name:             testTokenName,
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
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

			finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(
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

			transferTokenTransactionResponse, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 transfer token transaction")

			require.Equal(t, TokenTransactionVersion3, int(transferTokenTransactionResponse.Version), "final transfer transaction should be V3")
			require.Len(t, transferTokenTransactionResponse.TokenOutputs, 1, "expected 1 created output in V3 transfer transaction")

			verifyTokenBalance(t, userOutput3PrivKey, issuerPrivKey.Public(), testTransferOutput1Amount, "user three")
		})
	}
}

// TestCoordinatedTokenTransferWithMultipleTokenTypes tests transferring multiple token types in a single transaction
func TestCoordinatedTokenTransferWithMultipleTokenTypes(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			// Setup two different native spark tokens with mints
			token1, err := setupNativeTokenWithMint(t, "Token A", "TKA", 1000000, []uint64{1000})
			require.NoError(t, err, "failed to setup token 1")
			require.Len(t, token1.OutputOwners, 1, "expected 1 output owner for token 1")
			userPrivKey := token1.OutputOwners[0]

			token2, err := setupNativeTokenWithMint(t, "Token B", "TKB", 2000000, []uint64{2000})
			require.NoError(t, err, "failed to setup token 2")
			require.Len(t, token2.OutputOwners, 1, "expected 1 output owner for token 2")

			// Re-broadcast token 2's mint with the same user as owner for both tokens
			token2.MintTxBeforeBroadcast.TokenOutputs[0].OwnerPublicKey = userPrivKey.Public().Serialize()
			finalMintToken2, err := wallet.BroadcastTokenTransfer(
				t.Context(), token2.Config, token2.MintTxBeforeBroadcast,
				[]keys.Private{token2.IssuerPrivateKey},
			)
			require.NoError(t, err, "failed to broadcast updated mint transaction for token 2")
			mintToken2Hash, err := utils.HashTokenTransaction(finalMintToken2, false)
			require.NoError(t, err, "failed to hash mint transaction for token 2")

			// Create a transfer transaction that spends both token types and creates outputs in both token types
			recipient1PrivKey := keys.GeneratePrivateKey()
			recipient2PrivKey := keys.GeneratePrivateKey()

			multiTokenTransferTx := &tokenpb.TokenTransaction{
				Version: TokenTransactionVersion2,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: token1.MintTxHash,
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
						TokenIdentifier: token1.TokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, 600),
					},
					{
						OwnerPublicKey:  recipient2PrivKey.Public().Serialize(),
						TokenIdentifier: token1.TokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, 400),
					},
					{
						OwnerPublicKey:  recipient1PrivKey.Public().Serialize(),
						TokenIdentifier: token2.TokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, 1200),
					},
					{
						OwnerPublicKey:  recipient2PrivKey.Public().Serialize(),
						TokenIdentifier: token2.TokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, 800),
					},
				},
				Network:                         config.ProtoNetwork(),
				SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
				ClientCreatedTimestamp:          timestamppb.New(time.Now()),
			}

			finalTransferTx, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, multiTokenTransferTx,
				[]keys.Private{userPrivKey, userPrivKey},
			)
			require.NoError(t, err, "failed to broadcast multi-token transfer transaction")

			require.Len(t, finalTransferTx.TokenOutputs, 4, "expected 4 outputs in multi-token transfer")

			// Verify recipient 1 received correct amounts of both tokens
			verifyTokenBalance(t, recipient1PrivKey, token1.IssuerPrivateKey.Public(), 600, "recipient 1 token 1")
			verifyTokenBalance(t, recipient1PrivKey, token2.IssuerPrivateKey.Public(), 1200, "recipient 1 token 2")

			// Verify recipient 2 received correct amounts of both tokens
			verifyTokenBalance(t, recipient2PrivKey, token1.IssuerPrivateKey.Public(), 400, "recipient 2 token 1")
			verifyTokenBalance(t, recipient2PrivKey, token2.IssuerPrivateKey.Public(), 800, "recipient 2 token 2")

			// Verify token conservation: inputs of each type equal outputs of each type
			// Token A: 1000 (input) = 600 + 400 (outputs) ✓
			// Token B: 2000 (input) = 1200 + 800 (outputs) ✓
		})
	}
}
