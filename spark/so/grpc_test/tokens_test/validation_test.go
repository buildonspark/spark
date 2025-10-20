package tokens_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCoordinatedBroadcastTokenTransactionWithInvalidPrevTxHash(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentityPubKeyBytes := tokenPrivKey.Public().Serialize()
			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
			require.NoError(t, err, "failed to hash final issuance token transaction")

			corruptedHash := append(finalIssueTokenTransactionHash, 0xFF)

			transferTokenTransaction := &tokenpb.TokenTransaction{
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: corruptedHash,
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: finalIssueTokenTransactionHash,
								PrevTokenTransactionVout: 1,
							},
						},
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						OwnerPublicKey: userOutput1PrivKey.Public().Serialize(),
						TokenPublicKey: tokenIdentityPubKeyBytes,
						TokenAmount:    int64ToUint128Bytes(0, testTransferOutput1Amount),
					},
				},
				Network:                         config.ProtoNetwork(),
				SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
			}

			_, err = wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)

			require.Error(t, err, "expected transaction with invalid hash to be rejected")
		})
	}
}

func TestCoordinatedBroadcastTokenTransactionUnspecifiedNetwork(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction without a network to be rejected")
		})
	}
}

func TestCoordinatedBroadcastTokenTransactionTooLongValidityDuration(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = wallet.BroadcastCoordinatedTokenTransferWithExpiryDuration(
				t.Context(), config, issueTokenTransaction, TooLongValidityDurationSecs*time.Second,
				[]keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction with too long validity duration to be rejected")
		})
	}
}

func TestCoordinatedBroadcastTokenTransactionTooShortValidityDuration(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = wallet.BroadcastCoordinatedTokenTransferWithExpiryDuration(
				t.Context(), config, issueTokenTransaction, TooShortValidityDurationSecs*time.Second, []keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction with 0 validity duration to be rejected")
		})
	}
}

func TestCoordinatedQueryTokenOutputsByNetworkReturnsNoneForMismatchedNetwork(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, userOutput1PrivKey, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			_, err = wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			userOneConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userOutput1PrivKey)

			correctNetworkResponse, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userOneConfig,
				[]keys.Public{userOutput1PrivKey.Public()},
				[]keys.Public{tokenPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query token outputs")
			require.Len(t, correctNetworkResponse.OutputsWithPreviousTransactionData, 1, "expected one outputs when using the correct network")

			wrongNetworkConfig := userOneConfig
			wrongNetworkConfig.Network = common.Mainnet

			wrongNetworkResponse, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				wrongNetworkConfig,
				[]keys.Public{userOutput1PrivKey.Public()},
				[]keys.Public{tokenPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query token outputs")
			require.Empty(t, wrongNetworkResponse.OutputsWithPreviousTransactionData, "expected no outputs when using a different network")
		})
	}
}

func TestPartialTransactionValidationErrors(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
	tokenIdentityPubKey := config.IdentityPrivateKey.Public()

	testCases := []struct {
		name                string
		setupTx             func() (*tokenpb.TokenTransaction, []keys.Private)
		modifyTx            func(*tokenpb.TokenTransaction)
		expectedErrorSubstr string
	}{
		{
			name: "create transaction with creation entity public key should fail",
			setupTx: func() (*tokenpb.TokenTransaction, []keys.Private) {
				tx, err := createTestCoordinatedTokenCreateTransactionWithParams(config, createNativeSparkTokenParams{
					IssuerPrivateKey: config.IdentityPrivateKey,
					Name:             "Test Token",
					Ticker:           "TEST",
					MaxSupply:        1000000,
				})
				require.NoError(t, err)
				return tx, []keys.Private{config.IdentityPrivateKey}
			},
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.GetCreateInput().CreationEntityPublicKey = (&[33]byte{32: 1})[:]
			},
			expectedErrorSubstr: "creation entity public key will be added by the SO",
		},
		{
			name: "mint transaction with revocation commitment should fail",
			setupTx: func() (*tokenpb.TokenTransaction, []keys.Private) {
				tx, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey)
				require.NoError(t, err)
				return tx, []keys.Private{config.IdentityPrivateKey}
			},
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				tx.TokenOutputs[0].RevocationCommitment = (&[33]byte{32: 2})[:]
			},
			expectedErrorSubstr: "revocation commitment will be added by the SO",
		},
		{
			name: "mint transaction with withdraw bond sats should fail",
			setupTx: func() (*tokenpb.TokenTransaction, []keys.Private) {
				tx, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey)
				require.NoError(t, err)
				return tx, []keys.Private{config.IdentityPrivateKey}
			},
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				bondSats := uint64(10000)
				tx.TokenOutputs[0].WithdrawBondSats = &bondSats
			},
			expectedErrorSubstr: "withdraw bond sats will be added by the SO",
		},
		{
			name: "mint transaction with output ID should fail",
			setupTx: func() (*tokenpb.TokenTransaction, []keys.Private) {
				tx, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey)
				require.NoError(t, err)
				return tx, []keys.Private{config.IdentityPrivateKey}
			},
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				id := uuid.NewString()
				tx.TokenOutputs[0].Id = &id
			},
			expectedErrorSubstr: "ID will be added by the SO",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tokenTransaction, ownerPrivateKeys := tc.setupTx()
			tc.modifyTx(tokenTransaction)

			_, _, err := wallet.StartTokenTransactionCoordinated(
				t.Context(), config, tokenTransaction, ownerPrivateKeys, TestValidityDurationSecs*time.Second, nil,
			)

			require.ErrorContains(t, err, tc.expectedErrorSubstr, "error message should contain expected substring")
		})
	}
}

func TestCoordinatedTokenMintAndTransferTokensTooManyOutputsFails(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	tokenPrivKey := config.IdentityPrivateKey
	tooBigIssuanceTransaction, _, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
		tokenPrivKey.Public(), utils.MaxInputOrOutputTokenTransactionOutputs+1)
	require.NoError(t, err, "failed to create test token issuance transaction")

	_, err = wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, tooBigIssuanceTransaction, []keys.Private{tokenPrivKey},
	)
	require.Error(t, err, "expected error when broadcasting issuance transaction with more than utils.MaxInputOrOutputTokenTransactionOutputs=%d outputs", utils.MaxInputOrOutputTokenTransactionOutputs)
}

func TestCoordinatedTokenMintAndTransferTokensWithTooManyInputsFails(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransactionFirstBatch, userOutputPrivKeysFirstBatch, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
		tokenPrivKey.Public(), maxInputOrOutputTokenTransactionOutputsForTests)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransactionFirstBatch, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, issueTokenTransactionFirstBatch, []keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	issueTokenTransactionSecondBatch, userOutputPrivKeysSecondBatch, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t,
		config,
		tokenPrivKey.Public(), maxInputOrOutputTokenTransactionOutputsForTests)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransactionSecondBatch, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, issueTokenTransactionSecondBatch, []keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	finalIssueTokenTransactionHashFirstBatch, err := utils.HashTokenTransaction(finalIssueTokenTransactionFirstBatch, false)
	require.NoError(t, err, "failed to hash first issuance token transaction")

	finalIssueTokenTransactionHashSecondBatch, err := utils.HashTokenTransaction(finalIssueTokenTransactionSecondBatch, false)
	require.NoError(t, err, "failed to hash second issuance token transaction")

	consolidatedOutputPrivKey := keys.GeneratePrivateKey()

	consolidatedOutputPubKeyBytes := consolidatedOutputPrivKey.Public().Serialize()

	outputsToSpendTooMany := make([]*tokenpb.TokenOutputToSpend, 2*maxInputOrOutputTokenTransactionOutputsForTests)
	for i := 0; i < maxInputOrOutputTokenTransactionOutputsForTests; i++ {
		outputsToSpendTooMany[i] = &tokenpb.TokenOutputToSpend{
			PrevTokenTransactionHash: finalIssueTokenTransactionHashFirstBatch,
			PrevTokenTransactionVout: uint32(i),
		}
	}
	for i := 0; i < maxInputOrOutputTokenTransactionOutputsForTests; i++ {
		outputsToSpendTooMany[maxInputOrOutputTokenTransactionOutputsForTests+i] = &tokenpb.TokenOutputToSpend{
			PrevTokenTransactionHash: finalIssueTokenTransactionHashSecondBatch,
			PrevTokenTransactionVout: uint32(i),
		}
	}

	tooManyTransaction := &tokenpb.TokenTransaction{
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: outputsToSpendTooMany,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: consolidatedOutputPubKeyBytes,
				TokenPublicKey: tokenPrivKey.Public().Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, uint64(testIssueMultiplePerOutputAmount)*uint64(manyOutputsCount)),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
	}

	allUserOutputPrivKeys := append(userOutputPrivKeysFirstBatch, userOutputPrivKeysSecondBatch...)

	_, err = wallet.BroadcastCoordinatedTokenTransfer(t.Context(), config, tooManyTransaction, allUserOutputPrivKeys)
	require.Error(t, err, "expected error when broadcasting transfer transaction with more than MaxInputOrOutputTokenTransactionOutputsForTests=%d inputs", maxInputOrOutputTokenTransactionOutputsForTests)
}

func TestCoordinatedTokenMintAndTransferMaxInputsSucceeds(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutputPrivKeys, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
		tokenPrivKey.Public(), maxInputOrOutputTokenTransactionOutputsForTests)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash first issuance token transaction")

	consolidatedOutputPrivKey := keys.GeneratePrivateKey()
	consolidatedOutputPubKeyBytes := consolidatedOutputPrivKey.Public().Serialize()

	outputsToSpend := make([]*tokenpb.TokenOutputToSpend, maxInputOrOutputTokenTransactionOutputsForTests)
	for i := range outputsToSpend {
		outputsToSpend[i] = &tokenpb.TokenOutputToSpend{
			PrevTokenTransactionHash: finalIssueTokenTransactionHash,
			PrevTokenTransactionVout: uint32(i),
		}
	}
	consolidateTransaction := &tokenpb.TokenTransaction{
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: outputsToSpend,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: consolidatedOutputPubKeyBytes,
				TokenPublicKey: tokenPrivKey.Public().Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, uint64(testIssueMultiplePerOutputAmount)*uint64(maxInputOrOutputTokenTransactionOutputsForTests)),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		ClientCreatedTimestamp:          timestamppb.New(time.Now()),
	}

	_, err = wallet.BroadcastCoordinatedTokenTransfer(t.Context(), config, consolidateTransaction, userOutputPrivKeys)
	require.NoError(t, err, "failed to broadcast consolidation transaction")

	tokenOutputsResponse, err := wallet.QueryTokenOutputsV2(
		t.Context(),
		config,
		[]keys.Public{consolidatedOutputPrivKey.Public()},
		[]keys.Public{tokenPrivKey.Public()},
	)
	require.NoError(t, err, "failed to get owned token outputs")
	require.Len(t, tokenOutputsResponse.OutputsWithPreviousTransactionData, 1, "expected 1 consolidated output")
}
