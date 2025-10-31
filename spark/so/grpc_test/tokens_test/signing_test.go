package tokens_test

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// testCoordinatedTransactionSigningScenarios tests various signing scenarios for token transactions
func testCoordinatedTransactionSigningScenarios(
	t *testing.T,
	config *wallet.TestWalletConfig,
	tokenTransaction *tokenpb.TokenTransaction,
	startOwnerPrivateKeys []keys.Private,
	commitOwnerPrivateKeys []keys.Private,
	doubleStartSameTx bool,
	doubleStartDifferentTx bool,
	doubleCommit bool,
	expiredCommit bool,
	expectedStartError bool,
	expectedCommitError bool,
) *tokenpb.TokenTransaction {
	converted := make([]keys.Private, len(startOwnerPrivateKeys))
	for i, key := range startOwnerPrivateKeys {
		converted[i] = key
	}
	startResp, finalTxHash, startErr := wallet.StartTokenTransaction(
		t.Context(), config, tokenTransaction, converted, TestValidityDurationSecs*time.Second, nil,
	)

	if expectedStartError {
		require.Error(t, startErr, "expected start error but none")
		return nil
	} else {
		require.NoError(t, startErr, "unexpected start error")
	}

	if doubleStartSameTx {
		startResp2, finalTxHash2, startErr2 := wallet.StartTokenTransaction(
			t.Context(), config, tokenTransaction, converted, TestValidityDurationSecs*time.Second, nil,
		)
		require.NoError(t, startErr2, "unexpected error on second start")
		hash1, _ := utils.HashTokenTransaction(startResp.FinalTokenTransaction, false)
		hash2, _ := utils.HashTokenTransaction(startResp2.FinalTokenTransaction, false)
		require.Equal(t, finalTxHash, finalTxHash2, "final tx hashes should match on double start")
		require.Equal(t, hash1, hash2, "final transactions should hash identically after double start with same tx blob")
	}

	if doubleStartDifferentTx {
		tokenTransaction.ClientCreatedTimestamp = timestamppb.New(tokenTransaction.ClientCreatedTimestamp.AsTime().Add(-time.Second * 1))
		startResp2, finalTxHash2, startErr2 := wallet.StartTokenTransaction(
			t.Context(), config, tokenTransaction, converted, TestValidityDurationSecs*time.Second, nil,
		)
		require.NoError(t, startErr2, "unexpected error on second start")
		hash1, _ := utils.HashTokenTransaction(startResp.FinalTokenTransaction, false)
		hash2, _ := utils.HashTokenTransaction(startResp2.FinalTokenTransaction, false)
		require.NotEqual(t, finalTxHash, finalTxHash2, "final tx hashes should not match when double starting with different txs")
		require.NotEqual(t, hash1, hash2, "final transactions should hash differently for txs with different client created timestamp")

		txQueryParams := wallet.QueryTokenTransactionsParams{
			IssuerPublicKeys:  []keys.Public{},
			OwnerPublicKeys:   nil,
			OutputIDs:         nil,
			TransactionHashes: [][]byte{finalTxHash, finalTxHash2},
			Offset:            0,
			Limit:             2,
		}
		txQueryResponse, err := wallet.QueryTokenTransactions(
			t.Context(),
			config,
			txQueryParams,
		)
		require.NoError(t, err, "failed to query token transactions")
		require.Len(t, txQueryResponse.TokenTransactionsWithStatus, 2)

		startResp = startResp2
		finalTxHash = finalTxHash2
	}

	if expiredCommit {
		wait := time.Duration(TestValidityDurationSecsPlus1) * time.Second
		t.Logf("Waiting %v for transaction expiry", wait)
		time.Sleep(wait)
	}

	var operatorSignatures []*tokenpb.InputTtxoSignaturesPerOperator
	for _, operator := range config.SigningOperators {
		var ttxoSigs []*tokenpb.SignatureWithIndex
		for idx, privKey := range commitOwnerPrivateKeys {
			payloadHash, hashErr := utils.HashOperatorSpecificPayload(finalTxHash, operator.IdentityPublicKey)
			require.NoError(t, hashErr, "failed to hash operator-specific payload")

			sigBytes, sigErr := wallet.SignHashSlice(config, privKey, payloadHash)
			require.NoError(t, sigErr, "failed to create signature")

			ttxoSigs = append(ttxoSigs, &tokenpb.SignatureWithIndex{
				InputIndex: uint32(idx),
				Signature:  sigBytes,
			})
		}
		operatorSignatures = append(operatorSignatures, &tokenpb.InputTtxoSignaturesPerOperator{
			TtxoSignatures:            ttxoSigs,
			OperatorIdentityPublicKey: operator.IdentityPublicKey.Serialize(),
		})
	}

	commitReq := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          startResp.FinalTokenTransaction,
		FinalTokenTransactionHash:      finalTxHash,
		InputTtxoSignaturesPerOperator: operatorSignatures,
		OwnerIdentityPublicKey:         config.IdentityPublicKey().Serialize(),
	}

	commitResp, commitErr := wallet.CommitTransaction(t.Context(), config, commitReq)

	if expectedCommitError {
		require.Error(t, commitErr, "expected error during commit but none")
		return nil
	}
	require.NoError(t, commitErr)

	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, commitResp.CommitStatus)
	require.Nil(t, commitResp.CommitProgress, "commit progress should be nil")

	if doubleCommit {
		commitResp2, commitErr2 := wallet.CommitTransaction(t.Context(), config, commitReq)
		require.NoError(t, commitErr2, "unexpected error on second commit (double sign)")
		require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, commitResp2.CommitStatus)
	}

	return startResp.FinalTokenTransaction
}

// testCoordinatedMintTransactionSigningScenarios tests mint transaction signing scenarios
func testCoordinatedMintTransactionSigningScenarios(t *testing.T, config *wallet.TestWalletConfig,
	startIssuerPrivateKeys []keys.Private,
	commitIssuerPrivateKeys []keys.Private,
	isNativeSparkToken bool,
	useTokenIdentifier bool,
	doubleStartSameTx bool,
	doubleStartDifferentTx bool,
	doubleCommit bool,
	expiredCommit bool,
	expectedStartError bool,
	expectedCommitError bool,
) (*tokenpb.TokenTransaction, keys.Private, keys.Private) {
	if startIssuerPrivateKeys == nil {
		startIssuerPrivateKeys = []keys.Private{config.IdentityPrivateKey}
	}

	if commitIssuerPrivateKeys == nil {
		commitIssuerPrivateKeys = []keys.Private{config.IdentityPrivateKey}
	}

	tokenTransaction, userOutputPrivKeys, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: config.IdentityPrivateKey.Public(),
		IsNativeSparkToken:  isNativeSparkToken,
		UseTokenIdentifier:  useTokenIdentifier,
		NumOutputs:          2,
		OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
	})
	require.NoError(t, err, "failed to create test token mint transaction")
	require.Len(t, userOutputPrivKeys, 2)
	userOutput1PrivKey := userOutputPrivKeys[0]
	userOutput2PrivKey := userOutputPrivKeys[1]

	finalTx := testCoordinatedTransactionSigningScenarios(
		t,
		config,
		tokenTransaction,
		startIssuerPrivateKeys,
		commitIssuerPrivateKeys,
		doubleStartSameTx,
		doubleStartDifferentTx,
		doubleCommit,
		expiredCommit,
		expectedStartError,
		expectedCommitError,
	)

	if finalTx == nil {
		return nil, keys.Private{}, keys.Private{}
	}

	return finalTx, userOutput1PrivKey, userOutput2PrivKey
}

// testCoordinatedTransferTransactionSigningScenarios tests transfer transaction signing scenarios
func testCoordinatedTransferTransactionSigningScenarios(t *testing.T, config *wallet.TestWalletConfig,
	finalIssueTokenTransaction *tokenpb.TokenTransaction,
	startOwnerPrivateKeys []keys.Private,
	commitOwnerPrivateKeys []keys.Private,
	isNativeSparkToken bool,
	useTokenIdentifier bool,
	doubleStartSameTx bool,
	doubleStartDifferentTx bool,
	doubleCommit bool,
	expiredCommit bool,
	expectedStartError bool,
	expectedCommitError bool,
) {
	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            config.IdentityPrivateKey.Public(),
		IsNativeSparkToken:             isNativeSparkToken,
		UseTokenIdentifier:             useTokenIdentifier,
		FinalIssueTokenTransactionHash: finalIssueTokenTransactionHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	testCoordinatedTransactionSigningScenarios(
		t,
		config,
		transferTokenTransaction,
		startOwnerPrivateKeys,
		commitOwnerPrivateKeys,
		doubleStartSameTx,
		doubleStartDifferentTx,
		doubleCommit,
		expiredCommit,
		expectedStartError,
		expectedCommitError,
	)
}

// TestCoordinatedMintTransactionSigning tests various start/commit scenarios for token mint transactions
func TestCoordinatedMintTransactionSigning(t *testing.T) {
	testCases := []struct {
		name                     string
		issuerStartPrivateKeys   []keys.Private
		issuerCommitPrivateKeys  []keys.Private
		explicitWalletPrivateKey keys.Private
		createNativeSparkToken   bool
		useTokenIdentifier       bool
		expectedStartError       bool
		expectedCommitError      bool
		doubleStartSameTx        bool
		doubleStartDifferentTx   bool
		doubleCommit             bool
		expiredCommit            bool
	}{
		{
			name: "mint should succeed with l1 token without token identifier",
		},
		{
			name:               "mint should succeed with l1 token with token identifier",
			useTokenIdentifier: true,
		},
		{
			name:                     "mint should succeed with native spark token without token identifier",
			createNativeSparkToken:   true,
			explicitWalletPrivateKey: keys.GeneratePrivateKey(),
		},
		{
			name:                     "mint should succeed with native spark token with token identifier",
			createNativeSparkToken:   true,
			useTokenIdentifier:       true,
			explicitWalletPrivateKey: keys.GeneratePrivateKey(),
		},
		{
			name:                     "mint should fail with no associated token create",
			explicitWalletPrivateKey: keys.GeneratePrivateKey(),
			expectedStartError:       true,
		},
		{
			name: "mint should fail with too many issuer start signing keys",
			issuerStartPrivateKeys: []keys.Private{
				staticLocalIssuerKey.IdentityPrivateKey(),
				staticLocalIssuerKey.IdentityPrivateKey(),
			},
			expectedStartError: true,
		},
		{
			name:                   "mint should fail with incorrect issuer private key start step",
			issuerStartPrivateKeys: []keys.Private{keys.GeneratePrivateKey()},
			expectedStartError:     true,
		},
		{
			name: "mint should fail with too many issuer commit signing keys",
			issuerCommitPrivateKeys: []keys.Private{
				staticLocalIssuerKey.IdentityPrivateKey(),
				staticLocalIssuerKey.IdentityPrivateKey(),
			},
			expectedCommitError: true,
		},
		{
			name:                    "mint should fail with incorrect issuer private key commit step",
			issuerCommitPrivateKeys: []keys.Private{keys.GeneratePrivateKey()},
			expectedCommitError:     true,
		},
		{
			name:               "double start mint should succeed with same transaction",
			doubleStartSameTx:  true,
			useTokenIdentifier: true,
		},
		{
			name:                   "double start mint should succeed with different transaction",
			doubleStartDifferentTx: true,
		},
		{
			name:         "double commit mint should succeed with same transaction",
			doubleCommit: true,
		},
		{
			name:                "mint should fail with expired transaction",
			expiredCommit:       true,
			expectedCommitError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var issuerPrivateKey keys.Private
			if tc.explicitWalletPrivateKey != (keys.Private{}) {
				issuerPrivateKey = tc.explicitWalletPrivateKey
			} else {
				issuerPrivateKey = staticLocalIssuerKey.IdentityPrivateKey()
			}

			config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivateKey)

			if tc.createNativeSparkToken {
				err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
					issuerPrivateKey: issuerPrivateKey,
					name:             testTokenName,
					ticker:           testTokenTicker,
					maxSupply:        testTokenMaxSupply,
				})
				require.NoError(t, err, "failed to create native spark token")
			}

			testCoordinatedMintTransactionSigningScenarios(
				t, config,
				tc.issuerStartPrivateKeys,
				tc.issuerCommitPrivateKeys,
				tc.createNativeSparkToken,
				tc.useTokenIdentifier,
				tc.doubleStartSameTx,
				tc.doubleStartDifferentTx,
				tc.doubleCommit,
				tc.expiredCommit,
				tc.expectedStartError,
				tc.expectedCommitError)
		})
	}
}

// TestCoordinatedTransferTransactionSigning tests various start/commit scenarios for token transfer transactions
func TestCoordinatedTransferTransactionSigning(t *testing.T) {
	testCases := []struct {
		name                           string
		startOwnerPrivateKeysModifier  func([]keys.Private) []keys.Private
		explicitWalletPrivateKey       keys.Private
		createNativeSparkToken         bool
		useTokenIdentifier             bool
		commitOwnerPrivateKeysModifier func([]keys.Private) []keys.Private
		expectedStartError             bool
		expectedCommitError            bool
		doubleStartSameTx              bool
		doubleStartDifferentTx         bool
		doubleCommit                   bool
		expiredCommit                  bool
	}{
		{
			name: "transfer should succeed with l1 token",
		},
		{
			name:                     "transfer should succeed with native spark token without token identifier",
			createNativeSparkToken:   true,
			explicitWalletPrivateKey: keys.GeneratePrivateKey(),
		},
		{
			name:                     "transfer should succeed with native spark token with token identifier",
			createNativeSparkToken:   true,
			useTokenIdentifier:       true,
			explicitWalletPrivateKey: keys.GeneratePrivateKey(),
		},
		{
			name: "start should fail with reversing the owner signatures themselves",
			startOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[1], tokenOutputs[0]}
			},
			expectedStartError: true,
		},
		{
			name: "sign transfer should fail with duplicate operator specific owner signing private keys",
			commitOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[0], tokenOutputs[0]}
			},
			expectedCommitError: true,
		},
		{
			name: "sign transfer should fail with swapped owner signing private keys",
			commitOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[1], tokenOutputs[0]}
			},
			expectedCommitError: true,
		},
		{
			name: "sign transfer should fail with not enough owner signing keys",
			commitOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[0]}
			},
			expectedCommitError: true,
		},
		{
			name: "sign transfer should fail with too many owner signing keys",
			commitOwnerPrivateKeysModifier: func(tokenOutputs []keys.Private) []keys.Private {
				return []keys.Private{tokenOutputs[0], tokenOutputs[1], tokenOutputs[0]}
			},
			expectedCommitError: true,
		},
		{
			name:               "double start transfer should succeed with same transaction",
			doubleStartSameTx:  true,
			useTokenIdentifier: true,
		},
		{
			name:                   "double start transfer should succeed with different transaction",
			doubleStartDifferentTx: true,
		},
		{
			name:         "double commit transfer should succeed with same transaction",
			doubleCommit: true,
		},
		{
			name:                "sign transfer should fail with expired transaction",
			expiredCommit:       true,
			expectedCommitError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivateKey := tc.explicitWalletPrivateKey
			if issuerPrivateKey.IsZero() {
				issuerPrivateKey = staticLocalIssuerKey.IdentityPrivateKey()
			}

			config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivateKey)

			if tc.createNativeSparkToken {
				err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
					issuerPrivateKey: issuerPrivateKey,
					name:             testTokenName,
					ticker:           testTokenTicker,
					maxSupply:        testTokenMaxSupply,
				})
				require.NoError(t, err, "failed to create native spark token")
			}

			finalIssueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey := testCoordinatedMintTransactionSigningScenarios(
				t, config, nil, nil, tc.createNativeSparkToken, tc.useTokenIdentifier, false, false, false, false, false, false)

			defaultStartingOwnerPrivateKeys := []keys.Private{userOutput1PrivKey, userOutput2PrivKey}

			startingOwnerPrivKeys := defaultStartingOwnerPrivateKeys
			if tc.startOwnerPrivateKeysModifier != nil {
				startingOwnerPrivKeys = tc.startOwnerPrivateKeysModifier(defaultStartingOwnerPrivateKeys)
			}
			commitOwnerPrivKeys := startingOwnerPrivKeys
			if tc.commitOwnerPrivateKeysModifier != nil {
				commitOwnerPrivKeys = tc.commitOwnerPrivateKeysModifier(startingOwnerPrivKeys)
			}

			testCoordinatedTransferTransactionSigningScenarios(
				t, config, finalIssueTokenTransaction,
				startingOwnerPrivKeys,
				commitOwnerPrivKeys,
				tc.createNativeSparkToken,
				tc.useTokenIdentifier,
				tc.doubleStartSameTx,
				tc.doubleStartDifferentTx,
				tc.doubleCommit,
				tc.expiredCommit,
				tc.expectedStartError,
				tc.expectedCommitError,
			)
		})
	}
}
