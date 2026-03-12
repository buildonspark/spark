package tokens_test

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/protohash"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBroadcastTokenTransactionWithInvalidPrevTxHash(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())
			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err, "failed to create test token issuance transaction")

			finalIssueTokenTransaction, err := broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
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
						OwnerPublicKey:  userOutput1PrivKey.Public().Serialize(),
						TokenIdentifier: tokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, testTransferOutput1Amount),
					},
				},
				Network:                         config.ProtoNetwork(),
				SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
			}

			_, err = broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)

			require.Error(t, err, "expected transaction with invalid hash to be rejected")
		})
	}
}

func TestBroadcastTokenTransactionUnspecifiedNetwork(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction without a network to be rejected")
		})
	}
}

func TestBroadcastTokenTransactionTooLongValidityDuration(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = broadcastTokenTransactionWithValidityDuration(
				t,
				t.Context(),
				config,
				issueTokenTransaction,
				TooLongValidityDurationSecs*time.Second,
				[]keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction with too long validity duration to be rejected")
		})
	}
}

func TestBroadcastTokenTransactionTooShortValidityDuration(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = broadcastTokenTransactionWithValidityDuration(
				t,
				t.Context(),
				config,
				issueTokenTransaction,
				TooShortValidityDurationSecs*time.Second,
				[]keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction with 0 validity duration to be rejected")
		})
	}
}

func TestQueryTokenOutputsByNetworkReturnsNoneForMismatchedNetwork(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())
			issueTokenTransaction, userOutput1PrivKey, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err, "failed to create test token issuance transaction")

			_, err = broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			userOneConfig := wallet.NewTestWalletConfigWithIdentityKey(t, userOutput1PrivKey)

			correctNetworkResponse, err := wallet.QueryTokenOutputs(
				t.Context(),
				userOneConfig,
				[]keys.Public{userOutput1PrivKey.Public()},
				[]keys.Public{tokenPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query token outputs")
			require.Len(t, correctNetworkResponse.OutputsWithPreviousTransactionData, 1, "expected one outputs when using the correct network")

			wrongNetworkConfig := userOneConfig
			wrongNetworkConfig.Network = btcnetwork.Mainnet

			wrongNetworkResponse, err := wallet.QueryTokenOutputs(
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
	seededRng := rand.NewChaCha8([32]byte{})

	testCases := []struct {
		name                string
		setupTx             func() (*tokenpb.TokenTransaction, []keys.Private)
		modifyTx            func(*tokenpb.TokenTransaction)
		expectedErrorSubstr string
	}{
		{
			name: "create transaction with creation entity public key should fail",
			setupTx: func() (*tokenpb.TokenTransaction, []keys.Private) {
				tx, err := createTestTokenCreateTransactionWithParams(config, sparkTokenCreationTestParams{
					issuerPrivateKey: config.IdentityPrivateKey,
					name:             "Test Token",
					ticker:           "TEST",
					maxSupply:        1000000,
				})
				require.NoError(t, err)
				return tx, []keys.Private{config.IdentityPrivateKey}
			},
			modifyTx: func(tx *tokenpb.TokenTransaction) {
				privKey := keys.MustGeneratePrivateKeyFromRand(seededRng)
				tx.GetCreateInput().CreationEntityPublicKey = privKey.Public().Serialize()
			},
			expectedErrorSubstr: "creation entity public key will be added by the SO",
		},
		{
			name: "mint transaction with revocation commitment should fail",
			setupTx: func() (*tokenpb.TokenTransaction, []keys.Private) {
				tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenIdentityPubKey)
				tx, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey, tokenIdentifier)
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
				tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenIdentityPubKey)
				tx, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey, tokenIdentifier)
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
				tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenIdentityPubKey)
				tx, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey, tokenIdentifier)
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
		// TODO(CNT-589): Add explicit partial transaction validation integration tests for V3.
		if broadcastTokenTestsUseV3 {
			t.Skip("Skipping test for V3 transactions which requires these values be set by the client.")
		}
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			tokenTransaction, ownerPrivateKeys := tc.setupTx()
			tc.modifyTx(tokenTransaction)

			_, _, err := startTokenTransactionOrBroadcast(
				t,
				t.Context(),
				config,
				tokenTransaction,
				ownerPrivateKeys,
				TestValidityDurationSecs*time.Second,
			)

			require.ErrorContains(t, err, tc.expectedErrorSubstr, "error message should contain expected substring")
		})
	}
}

func TestTokenMintWithWrongIssuerPublicKeyFails(t *testing.T) {
	runSignatureTypeTestCases(t, func(t *testing.T, tc signatureTypeTestCase) {
		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

		tokenPrivKey := config.IdentityPrivateKey
		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		// Generate a different key to use as the issuer public key in the MintInput
		wrongKey := keys.GeneratePrivateKey()

		mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: wrongKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{uint64(testIssueOutput1Amount)},
		})
		require.NoError(t, err)

		_, err = broadcastTokenTransaction(
			t,
			t.Context(),
			config,
			mintTx,
			[]keys.Private{wrongKey},
		)
		require.Error(t, err, "expected mint with wrong issuer public key to be rejected")
		require.ErrorContains(t, err, "issuer key mismatch")
	})
}

func TestTokenMintUsesCorrectIssuerPublicKey(t *testing.T) {
	runSignatureTypeTestCases(t, func(t *testing.T, tc signatureTypeTestCase) {
		// Use a fresh issuer key (not the static one) to demonstrate that the SO resolves
		// the issuer public key from the TokenCreate record via the token identifier.
		issuerPrivKey := keys.GeneratePrivateKey()
		config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)
		config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

		err := testCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
			issuerPrivateKey: issuerPrivKey,
			name:             "Key Test Token",
			ticker:           "KTT",
			maxSupply:        0,
		})
		require.NoError(t, err, "failed to create token")

		// Query the token metadata to get the issuer public key as stored in the SO's TokenCreate record.
		resp, err := wallet.QueryTokenMetadata(t.Context(), config, nil, []keys.Public{issuerPrivKey.Public()})
		require.NoError(t, err, "failed to query token metadata")
		require.Len(t, resp.TokenMetadata, 1, "expected exactly one token")
		require.Equal(t, issuerPrivKey.Public().Serialize(), resp.TokenMetadata[0].IssuerPublicKey,
			"issuer public key in token metadata should match the key used to create the token")

		tokenIdentifier := resp.TokenMetadata[0].TokenIdentifier

		mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: issuerPrivKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{uint64(testIssueOutput1Amount)},
		})
		require.NoError(t, err, "failed to create mint transaction")

		// Mint succeeds because the SO resolves the issuer public key from the TokenCreate record
		// (via the token identifier) and correctly validates the signature.
		_, err = broadcastTokenTransaction(t, t.Context(), config, mintTx, []keys.Private{issuerPrivKey})
		require.NoError(t, err, "mint should succeed when the correct issuer key (resolved from TokenCreate via token identifier) is used")
	})
}

func TestTokenMintWithBadSignatureFails(t *testing.T) {
	runSignatureTypeTestCases(t, func(t *testing.T, tc signatureTypeTestCase) {
		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

		tokenPrivKey := config.IdentityPrivateKey
		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		// Create a mint transaction with the correct issuer public key in MintInput,
		// matching the TokenCreate record, so the key mismatch check passes.
		mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: tokenPrivKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{uint64(testIssueOutput1Amount)},
		})
		require.NoError(t, err, "failed to create mint transaction")

		// Sign with a completely different key, producing an invalid signature.
		// The SO resolves the issuer public key from the TokenCreate record and
		// uses it to verify the signature — which should fail.
		wrongSigningKey := keys.GeneratePrivateKey()
		_, err = broadcastTokenTransaction(t, t.Context(), config, mintTx, []keys.Private{wrongSigningKey})
		require.Error(t, err, "expected mint with bad signature to be rejected")
		require.ErrorContains(t, err, "failed to validate mint token transaction signature")
	})
}

func TestTokenMintAndTransferTokensTooManyOutputsFails(t *testing.T) {
	runSignatureTypeTestCases(t, func(t *testing.T, tc signatureTypeTestCase) {
		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

		tokenPrivKey := config.IdentityPrivateKey
		tooBigIssuanceTransaction, _, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
			tokenPrivKey.Public(), utils.MaxInputOrOutputTokenTransactionOutputs+1)
		require.NoError(t, err, "failed to create test token issuance transaction")

		_, err = broadcastTokenTransaction(
			t,
			t.Context(),
			config,
			tooBigIssuanceTransaction,
			[]keys.Private{tokenPrivKey},
		)
		require.Error(t, err, "expected error when broadcasting issuance transaction with more than utils.MaxInputOrOutputTokenTransactionOutputs=%d outputs", utils.MaxInputOrOutputTokenTransactionOutputs)
	})
}

func TestTokenMintAndTransferTokensWithTooManyInputsFails(t *testing.T) {
	runSignatureTypeTestCases(t, func(t *testing.T, tc signatureTypeTestCase) {
		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures
		tokenPrivKey := config.IdentityPrivateKey
		issueTokenTransactionFirstBatch, userOutputPrivKeysFirstBatch, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
			tokenPrivKey.Public(), maxInputOrOutputTokenTransactionOutputsForTests)
		require.NoError(t, err, "failed to create test token issuance transaction")

		finalIssueTokenTransactionFirstBatch, err := broadcastTokenTransaction(
			t,
			t.Context(),
			config,
			issueTokenTransactionFirstBatch,
			[]keys.Private{tokenPrivKey},
		)
		require.NoError(t, err, "failed to broadcast issuance token transaction")

		issueTokenTransactionSecondBatch, userOutputPrivKeysSecondBatch, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t,
			config,
			tokenPrivKey.Public(), maxInputOrOutputTokenTransactionOutputsForTests)
		require.NoError(t, err, "failed to create test token issuance transaction")

		finalIssueTokenTransactionSecondBatch, err := broadcastTokenTransaction(
			t,
			t.Context(),
			config,
			issueTokenTransactionSecondBatch,
			[]keys.Private{tokenPrivKey},
		)
		require.NoError(t, err, "failed to broadcast issuance token transaction")

		finalIssueTokenTransactionHashFirstBatch, err := utils.HashTokenTransaction(finalIssueTokenTransactionFirstBatch, false)
		require.NoError(t, err, "failed to hash first issuance token transaction")

		finalIssueTokenTransactionHashSecondBatch, err := utils.HashTokenTransaction(finalIssueTokenTransactionSecondBatch, false)
		require.NoError(t, err, "failed to hash second issuance token transaction")

		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		consolidatedOutputPrivKey := keys.GeneratePrivateKey()

		outputsToSpendTooMany := make([]*tokenpb.TokenOutputToSpend, 2*maxInputOrOutputTokenTransactionOutputsForTests)
		for i := range maxInputOrOutputTokenTransactionOutputsForTests {
			outputsToSpendTooMany[i] = &tokenpb.TokenOutputToSpend{
				PrevTokenTransactionHash: finalIssueTokenTransactionHashFirstBatch,
				PrevTokenTransactionVout: uint32(i),
			}
		}
		for i := range maxInputOrOutputTokenTransactionOutputsForTests {
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
					OwnerPublicKey:  consolidatedOutputPrivKey.Public().Serialize(),
					TokenIdentifier: tokenIdentifier,
					TokenAmount:     int64ToUint128Bytes(0, uint64(testIssueMultiplePerOutputAmount)*uint64(manyOutputsCount)),
				},
			},
			Network:                         config.ProtoNetwork(),
			SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		}

		allUserOutputPrivKeys := append(userOutputPrivKeysFirstBatch, userOutputPrivKeysSecondBatch...)

		_, err = broadcastTokenTransaction(
			t,
			t.Context(),
			config,
			tooManyTransaction,
			allUserOutputPrivKeys,
		)
		require.Error(t, err, "expected error when broadcasting transfer transaction with more than MaxInputOrOutputTokenTransactionOutputsForTests=%d inputs", maxInputOrOutputTokenTransactionOutputsForTests)
	})
}

func TestTokenMintAndTransferMaxInputsSucceeds(t *testing.T) {
	sparktesting.SkipIfGithubActions(t)
	runSignatureTypeTestCases(t, func(t *testing.T, tc signatureTypeTestCase) {
		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

		tokenPrivKey := config.IdentityPrivateKey
		issueTokenTransaction, userOutputPrivKeys, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
			tokenPrivKey.Public(), maxInputOrOutputTokenTransactionOutputsForTests)
		require.NoError(t, err, "failed to create test token issuance transaction")

		finalIssueTokenTransaction, err := broadcastTokenTransaction(
			t,
			t.Context(),
			config,
			issueTokenTransaction,
			[]keys.Private{tokenPrivKey},
		)
		require.NoError(t, err, "failed to broadcast issuance token transaction")

		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
		require.NoError(t, err, "failed to hash first issuance token transaction")

		consolidatedOutputPrivKey := keys.GeneratePrivateKey()
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
					OwnerPublicKey:  consolidatedOutputPrivKey.Public().Serialize(),
					TokenIdentifier: tokenIdentifier,
					TokenAmount:     int64ToUint128Bytes(0, uint64(testIssueMultiplePerOutputAmount)*uint64(maxInputOrOutputTokenTransactionOutputsForTests)),
				},
			},
			Network:                         config.ProtoNetwork(),
			SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
			ClientCreatedTimestamp:          timestamppb.New(time.Now()),
		}
		_, err = broadcastTokenTransaction(
			t,
			t.Context(),
			config,
			consolidateTransaction,
			userOutputPrivKeys,
		)
		require.NoError(t, err, "failed to broadcast consolidation transaction")

		tokenOutputsResponse, err := wallet.QueryTokenOutputs(
			t.Context(),
			config,
			[]keys.Public{consolidatedOutputPrivKey.Public()},
			[]keys.Public{tokenPrivKey.Public()},
		)
		require.NoError(t, err, "failed to get owned token outputs")
		require.Len(t, tokenOutputsResponse.OutputsWithPreviousTransactionData, 1, "expected 1 consolidated output")
	})
}

func TestBroadcastTokenTransactionV3ValidationRules(t *testing.T) {
	if !broadcastTokenTestsUseV3 {
		t.Skip("Skipping test for V2 transactions which does not require these values be set by the client.")
	}

	testCases := []struct {
		name                  string
		mutatePartial         func(*tokenpb.PartialTokenTransaction)
		expectedErrorContains string
	}{
		{
			name: "nanosecond_precision_timestamp_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				nanoPrecisionTimestamp := time.Now()
				if nanoPrecisionTimestamp.Nanosecond()%1000 == 0 {
					nanoPrecisionTimestamp = nanoPrecisionTimestamp.Add(123 * time.Nanosecond)
				}
				partial.TokenTransactionMetadata.ClientCreatedTimestamp = timestamppb.New(nanoPrecisionTimestamp)
			},
			expectedErrorContains: "sub-microsecond precision",
		},
		{
			name: "out_of_order_operator_keys_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				reversedKeys := slices.Clone(partial.TokenTransactionMetadata.GetSparkOperatorIdentityPublicKeys())
				slices.Reverse(reversedKeys)
				partial.TokenTransactionMetadata.SparkOperatorIdentityPublicKeys = reversedKeys
			},
			expectedErrorContains: "strictly bytewise ascending",
		},
		{
			name: "out_of_order_invoice_attachments_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				reversedInvoices := slices.Clone(partial.TokenTransactionMetadata.GetInvoiceAttachments())
				slices.Reverse(reversedInvoices)
				partial.TokenTransactionMetadata.InvoiceAttachments = reversedInvoices
			},
			expectedErrorContains: "strictly ascending by spark_invoice",
		},
		{
			name: "nil_metadata_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				partial.TokenTransactionMetadata = nil
			},
			expectedErrorContains: "token transaction metadata cannot be nil",
		},
		{
			name: "nil_invoice_attachment_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				partial.TokenTransactionMetadata.InvoiceAttachments = append(
					partial.TokenTransactionMetadata.GetInvoiceAttachments(),
					nil,
				)
			},
			expectedErrorContains: "invoice_attachments must not contain nil or empty entries",
		},
		{
			name: "duplicate_operator_keys_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				keys := partial.TokenTransactionMetadata.GetSparkOperatorIdentityPublicKeys()
				if len(keys) > 0 {
					partial.TokenTransactionMetadata.SparkOperatorIdentityPublicKeys = append(keys, keys[0])
				}
			},
			expectedErrorContains: "strictly bytewise ascending",
		},
		{
			name: "duplicate_invoice_strings_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				invoices := partial.TokenTransactionMetadata.GetInvoiceAttachments()
				if len(invoices) > 0 {
					duplicate := &tokenpb.InvoiceAttachment{
						SparkInvoice: invoices[0].GetSparkInvoice(),
					}
					partial.TokenTransactionMetadata.InvoiceAttachments = append(invoices, duplicate)
				}
			},
			expectedErrorContains: "strictly ascending by spark_invoice",
		},
		{
			name: "zero_validity_duration_rejected",
			mutatePartial: func(partial *tokenpb.PartialTokenTransaction) {
				partial.TokenTransactionMetadata.ValidityDurationSeconds = 0
			},
			expectedErrorContains: "value must be inside range [1, 300]",
		},
	}

	for _, tc := range testCases {
		for _, sigTC := range signatureTypeTestCases {
			testName := tc.name + "_" + sigTC.name + "_[" + currentBroadcastRunLabel() + "]"
			t.Run(testName, func(t *testing.T) {
				config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
				config.UseTokenTransactionSchnorrSignatures = sigTC.useSchnorrSignatures

				tokenPrivKey := config.IdentityPrivateKey
				tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())
				issueTokenTransaction, userOutput1PrivKey, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(),
					tokenIdentifier)
				require.NoError(t, err, "failed to create test token issuance transaction")

				finalIssueTokenTransaction, err := broadcastTokenTransaction(
					t,
					t.Context(),
					config,
					issueTokenTransaction,
					[]keys.Private{tokenPrivKey},
				)
				require.NoError(t, err, "failed to broadcast issuance token transaction")

				finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
				require.NoError(t, err, "failed to hash final issuance token transaction")

				transferTokenTransaction := &tokenpb.TokenTransaction{
					Version:                         3,
					Network:                         config.ProtoNetwork(),
					SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
					ValidityDurationSeconds:         proto.Uint64(180),
					TokenInputs: &tokenpb.TokenTransaction_TransferInput{
						TransferInput: &tokenpb.TokenTransferInput{
							OutputsToSpend: []*tokenpb.TokenOutputToSpend{
								{
									PrevTokenTransactionHash: finalIssueTokenTransactionHash,
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
							OwnerPublicKey:  userOutput1PrivKey.Public().Serialize(),
							TokenIdentifier: tokenIdentifier,
							TokenAmount:     int64ToUint128Bytes(0, testTransferOutput1Amount),
						},
					},
					InvoiceAttachments: []*tokenpb.InvoiceAttachment{
						{SparkInvoice: "spark:abc123..."},
						{SparkInvoice: "spark:xyz789..."},
					},
				}

				partialTx, err := protoconverter.ConvertV2TxShapeToPartial(transferTokenTransaction)
				require.NoError(t, err, "failed to convert to partial")

				normalizeV3PartialTokenTransaction(partialTx)

				tc.mutatePartial(partialTx)

				partialHash, err := protohash.Hash(partialTx)
				require.NoError(t, err, "failed to hash partial")

				ownerSignatures := make([]*tokenpb.SignatureWithIndex, 0)
				for i, privKey := range []keys.Private{userOutput1PrivKey, userOutput1PrivKey} {
					sig, err := wallet.SignHashSlice(config, privKey, partialHash)
					require.NoError(t, err, "failed to sign")
					ownerSignatures = append(ownerSignatures, &tokenpb.SignatureWithIndex{
						InputIndex: uint32(i),
						Signature:  sig,
					})
				}

				req := &tokenpb.BroadcastTransactionRequest{
					IdentityPublicKey:               config.IdentityPublicKey().Serialize(),
					PartialTokenTransaction:         partialTx,
					TokenTransactionOwnerSignatures: ownerSignatures,
				}
				_, err = wallet.BroadcastTokenTransactionV3Request(t.Context(), config, req)

				require.Error(t, err, "expected transaction to be rejected")
				require.Contains(t, err.Error(), tc.expectedErrorContains)
			})
		}
	}
}

func TestBroadcastTokenTransactionV3ExecuteBeforeValidation(t *testing.T) {
	if !broadcastTokenTestsUseV3 {
		t.Skip("execute_before is only validated in V3 broadcast")
	}
	if !broadcastTokenTestsUsePhase2 {
		t.Skip("execute_before is only validated in V3 Phase2")
	}

	testCases := []struct {
		name                  string
		setExecuteBefore      func(partial *tokenpb.PartialTokenTransaction)
		expectError           bool
		expectedErrorContains string
	}{
		{
			name: "valid_execute_before_within_window",
			setExecuteBefore: func(partial *tokenpb.PartialTokenTransaction) {
				clientTs := partial.TokenTransactionMetadata.GetClientCreatedTimestamp().AsTime()
				validTime := clientTs.Add(5 * time.Minute)
				validTime = utils.ToMicrosecondPrecision(validTime)
				partial.ExecuteBefore = timestamppb.New(validTime)
			},
			expectError: false,
		},
		{
			name: "execute_before_expired_rejected",
			setExecuteBefore: func(partial *tokenpb.PartialTokenTransaction) {
				// Set client_created_timestamp to 10 seconds ago so execute_before
				// can be after it but still in the past (expired).
				pastClient := utils.ToMicrosecondPrecision(time.Now().Add(-10 * time.Second))
				partial.TokenTransactionMetadata.ClientCreatedTimestamp = timestamppb.New(pastClient)
				expiredTime := utils.ToMicrosecondPrecision(pastClient.Add(1 * time.Second))
				partial.ExecuteBefore = timestamppb.New(expiredTime)
			},
			expectError:           true,
			expectedErrorContains: "has already passed",
		},
		{
			name: "execute_before_beyond_max_window_rejected",
			setExecuteBefore: func(partial *tokenpb.PartialTokenTransaction) {
				clientTs := partial.TokenTransactionMetadata.GetClientCreatedTimestamp().AsTime()
				// TokenMaxExecuteBeforeWindow is 14 days; set to 15 days
				tooFar := clientTs.Add(15 * 24 * time.Hour)
				tooFar = utils.ToMicrosecondPrecision(tooFar)
				partial.ExecuteBefore = timestamppb.New(tooFar)
			},
			expectError:           true,
			expectedErrorContains: "exceeds max window",
		},
		{
			name: "execute_before_at_client_created_timestamp_rejected",
			setExecuteBefore: func(partial *tokenpb.PartialTokenTransaction) {
				clientTs := partial.TokenTransactionMetadata.GetClientCreatedTimestamp().AsTime()
				partial.ExecuteBefore = timestamppb.New(clientTs)
			},
			expectError:           true,
			expectedErrorContains: "must be after client_created_timestamp",
		},
		{
			name: "execute_before_before_client_created_timestamp_rejected",
			setExecuteBefore: func(partial *tokenpb.PartialTokenTransaction) {
				clientTs := partial.TokenTransactionMetadata.GetClientCreatedTimestamp().AsTime()
				beforeClient := utils.ToMicrosecondPrecision(clientTs.Add(-1 * time.Second))
				partial.ExecuteBefore = timestamppb.New(beforeClient)
			},
			expectError:           true,
			expectedErrorContains: "must be after client_created_timestamp",
		},
		{
			name: "execute_before_with_sub_microsecond_precision_rejected",
			setExecuteBefore: func(partial *tokenpb.PartialTokenTransaction) {
				clientTs := partial.TokenTransactionMetadata.GetClientCreatedTimestamp().AsTime()
				nanoTime := clientTs.Add(5 * time.Minute).Add(123 * time.Nanosecond)
				if nanoTime.Nanosecond()%1000 == 0 {
					nanoTime = nanoTime.Add(1 * time.Nanosecond)
				}
				partial.ExecuteBefore = timestamppb.New(nanoTime)
			},
			expectError:           true,
			expectedErrorContains: "sub-microsecond precision",
		},
	}

	for _, tc := range testCases {
		for _, sigTC := range signatureTypeTestCases {
			testName := tc.name + "_" + sigTC.name + "_[" + currentBroadcastRunLabel() + "]"
			t.Run(testName, func(t *testing.T) {
				issuerPrivKey := keys.GeneratePrivateKey()
				config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)
				config.UseTokenTransactionSchnorrSignatures = sigTC.useSchnorrSignatures

				err := testCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
					issuerPrivateKey: issuerPrivKey,
					name:             "ExecBefore Token",
					ticker:           "EBT",
					maxSupply:        0,
				})
				require.NoError(t, err, "failed to create native spark token")

				tokenIdentifier := queryTokenIdentifierOrFail(t, config, issuerPrivKey.Public())
				issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public(), tokenIdentifier)
				require.NoError(t, err, "failed to create test token issuance transaction")

				finalIssueTokenTransaction, err := broadcastTokenTransaction(
					t, t.Context(), config, issueTokenTransaction, []keys.Private{issuerPrivKey},
				)
				require.NoError(t, err, "failed to broadcast issuance token transaction")

				finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
				require.NoError(t, err, "failed to hash final issuance token transaction")

				withdrawBond := uint64(withdrawalBondSatsInConfig)
				withdrawLock := uint64(withdrawalRelativeBlockLocktimeInConfig)
				transferTokenTransaction := &tokenpb.TokenTransaction{
					Version:                         3,
					Network:                         config.ProtoNetwork(),
					SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
					ValidityDurationSeconds:         proto.Uint64(180),
					ClientCreatedTimestamp:          timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC())),
					TokenInputs: &tokenpb.TokenTransaction_TransferInput{
						TransferInput: &tokenpb.TokenTransferInput{
							OutputsToSpend: []*tokenpb.TokenOutputToSpend{
								{
									PrevTokenTransactionHash: finalIssueTokenTransactionHash,
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
							OwnerPublicKey:                userOutput1PrivKey.Public().Serialize(),
							TokenIdentifier:               tokenIdentifier,
							TokenAmount:                   int64ToUint128Bytes(0, testTransferOutput1Amount),
							WithdrawBondSats:              &withdrawBond,
							WithdrawRelativeBlockLocktime: &withdrawLock,
						},
					},
				}
				normalizeV3TokenTransaction(transferTokenTransaction)

				partialTx, err := protoconverter.ConvertV2TxShapeToPartial(transferTokenTransaction)
				require.NoError(t, err, "failed to convert to partial")

				normalizeV3PartialTokenTransaction(partialTx)

				// Sign using the V2 shape hash BEFORE setting execute_before on the partial.
				// The server converts Partial → V2 → Partial for signature verification,
				// which strips execute_before, so signatures must be over the V2 shape hash.
				v2Hash, err := utils.HashTokenTransaction(transferTokenTransaction, true)
				require.NoError(t, err, "failed to hash V2 shape for signing")

				tc.setExecuteBefore(partialTx)

				ownerSignatures := make([]*tokenpb.SignatureWithIndex, 0)
				for i, privKey := range []keys.Private{userOutput1PrivKey, userOutput2PrivKey} {
					sig, err := wallet.SignHashSlice(config, privKey, v2Hash)
					require.NoError(t, err, "failed to sign")
					ownerSignatures = append(ownerSignatures, &tokenpb.SignatureWithIndex{
						InputIndex: uint32(i),
						Signature:  sig,
					})
				}

				req := &tokenpb.BroadcastTransactionRequest{
					IdentityPublicKey:               config.IdentityPublicKey().Serialize(),
					PartialTokenTransaction:         partialTx,
					TokenTransactionOwnerSignatures: ownerSignatures,
				}

				_, err = wallet.BroadcastTokenTransactionV3Request(t.Context(), config, req)

				if tc.expectError {
					require.Error(t, err, "expected transaction to be rejected")
					require.Contains(t, err.Error(), tc.expectedErrorContains)
				} else {
					require.NoError(t, err, "expected transaction to succeed")
				}
			})
		}
	}
}
