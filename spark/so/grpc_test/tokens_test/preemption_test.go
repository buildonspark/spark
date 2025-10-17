package tokens_test

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCoordinatedTokenTransferPreemption(t *testing.T) {
	coordinatorScenarios := []CoordinatorScenario{
		{
			name:            "different coordinators",
			sameCoordinator: false,
		},
		{
			name:            "same coordinator",
			sameCoordinator: true,
		},
	}

	timestampScenarios := []TimestampScenario{
		{
			name:          "timestamp-based pre-emption - first earlier",
			timestampMode: TimestampScenarioFirstEarlier,
		},
		{
			name:          "timestamp-based pre-emption - second earlier",
			timestampMode: TimestampScenarioSecondEarlier,
		},
		{
			name:          "expired transaction pre-emption",
			timestampMode: TimestampScenarioExpired,
		},
	}

	secondRequestScenarios := []SecondRequestScenario{
		{
			name:                  "second request after Start()",
			secondRequestScenario: SecondRequestScenarioAfterStart,
		},
		{
			name:                  "second request after SignTokenTransactionFromCoordination()",
			secondRequestScenario: SecondRequestScenarioAfterSignTokenTransactionFromCoordination,
		},
	}

	var testCases []PreemptionTestCase

	for _, coordTC := range coordinatorScenarios {
		for _, timeTC := range timestampScenarios {
			for _, secondRequestTC := range secondRequestScenarios {
				testCases = append(testCases, PreemptionTestCase{
					name:                  coordTC.name + " - " + timeTC.name + " - " + secondRequestTC.name,
					sameCoordinator:       coordTC.sameCoordinator,
					timestampMode:         timeTC.timestampMode,
					secondRequestScenario: secondRequestTC.secondRequestScenario,
				})
			}
		}
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentityPubKey := tokenPrivKey.Public()

			config1 := config
			var config2 *wallet.TestWalletConfig
			if tc.sameCoordinator {
				config2 = config
			} else {
				config2 = wallet.NewTestWalletConfigWithParams(t,
					wallet.TestWalletConfigParams{
						IdentityPrivateKey: staticLocalIssuerKey.IdentityPrivateKey(),
						CoordinatorIndex:   1,
					},
				)
			}

			mintTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config1, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create mint transaction for transfer test")

			finalMintTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config1, mintTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast mint transaction for transfer test")

			finalMintTransactionHash, err := utils.HashTokenTransaction(finalMintTransaction, false)
			require.NoError(t, err, "failed to hash mint transaction")

			transaction1, _, err := createTestTokenTransferTransactionTokenPb(t, config1, finalMintTransactionHash, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create first transfer transaction")

			transaction2, _, err := createTestTokenTransferTransactionTokenPb(t, config2, finalMintTransactionHash, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create second transfer transaction")

			setTransactionTimestamps(transaction1, transaction2, tc.timestampMode)

			txPartialHash1, err := utils.HashTokenTransaction(transaction1, true)
			require.NoError(t, err, "failed to hash first transfer transaction")
			txPartialHash2, err := utils.HashTokenTransaction(transaction2, true)
			require.NoError(t, err, "failed to hash second transfer transaction")

			t1ExpiryDuration := 180 * time.Second
			if tc.timestampMode == TimestampScenarioExpired {
				t1ExpiryDuration = time.Second
			}

			resp1, resp1Hash, err := wallet.StartTokenTransactionCoordinated(t.Context(), config1, transaction1, []keys.Private{userOutput1PrivKey, userOutput2PrivKey}, t1ExpiryDuration, nil)
			require.NoError(t, err, "failed to start first transaction")
			require.NotNil(t, resp1)

			queryAndVerifyTokenOutputs(t, []string{config1.CoordinatorIdentifier, config2.CoordinatorIdentifier}, finalMintTransaction, userOutput1PrivKey)

			if tc.secondRequestScenario == SecondRequestScenarioAfterSignTokenTransactionFromCoordination {
				nonCoordinatorOperator := config1.SigningOperators["0000000000000000000000000000000000000000000000000000000000000003"]
				require.NotNil(t, nonCoordinatorOperator, "expected a non-coordinator operator")
				_, err := wallet.SignTokenTransactionFromCoordination(t.Context(), config2, wallet.SignTokenTransactionFromCoordinationParams{
					Operator:         nonCoordinatorOperator,
					TokenTransaction: resp1.FinalTokenTransaction,
					FinalTxHash:      resp1Hash,
					OwnerPrivateKeys: []keys.Private{userOutput1PrivKey, userOutput2PrivKey},
				})
				require.NoError(t, err, "failed to sign first transaction with non-coordinator operator %s", nonCoordinatorOperator.Identifier)
				queryAndVerifyTokenOutputs(t, []string{config1.CoordinatorIdentifier, config2.CoordinatorIdentifier}, finalMintTransaction, userOutput1PrivKey)
			}

			if tc.timestampMode == TimestampScenarioExpired {
				time.Sleep(time.Second * 1)
			}

			resp2, resp2Hash, err := wallet.StartTokenTransactionCoordinated(t.Context(), config2, transaction2, []keys.Private{userOutput1PrivKey, userOutput2PrivKey}, 180*time.Second, nil)
			queryAndVerifyTokenOutputs(t, []string{config1.CoordinatorIdentifier, config2.CoordinatorIdentifier}, finalMintTransaction, userOutput1PrivKey)

			winningResult, losingResult := determineWinningAndLosingTransactions(
				tc,
				&TransactionResult{config: config1, resp: resp1, txFullHash: resp1Hash, txPartialHash: txPartialHash1},
				&TransactionResult{config: config2, resp: resp2, txFullHash: resp2Hash, txPartialHash: txPartialHash2},
			)

			if losingResult != nil {
				require.NoError(t, err, "expected second transaction to succeed and pre-empt the first")
				require.NotNil(t, resp2, "expected non-nil response when transaction pre-empts")
				_, err := signAndCommitTransaction(t, losingResult, []keys.Private{userOutput1PrivKey, userOutput2PrivKey})
				require.Error(t, err, "expected losing transaction to fail to commit due to being cancelled")
			} else {
				require.Error(t, err, "expected second transaction to be rejected due to pre-emption")
				require.Nil(t, resp2, "expected nil response when transaction is pre-empted")

				stat, ok := status.FromError(err)
				require.True(t, ok, "expected error to be a gRPC status error")
				require.Equal(t, codes.Aborted, stat.Code(), "expected gRPC status code to be Aborted when transaction is pre-empted")
			}

			_, err = signAndCommitTransaction(t, winningResult, []keys.Private{userOutput1PrivKey, userOutput2PrivKey})
			require.NoError(t, err, "expected winning transaction to commit")
			queryAndVerifyNoTokenOutputs(t, []string{config1.CoordinatorIdentifier, config2.CoordinatorIdentifier}, userOutput1PrivKey)
		})
	}
}

func TestCoordinatedTokenTransferPreemptionPreventionRevealed(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKey := tokenPrivKey.Public()

	mintTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey)
	require.NoError(t, err, "failed to create mint transaction for transfer test")

	finalMintTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTransaction,
		[]keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast mint transaction for transfer test")

	finalMintTransactionHash, err := utils.HashTokenTransaction(finalMintTransaction, false)
	require.NoError(t, err, "failed to hash mint transaction")

	transaction1, _, err := createTestTokenTransferTransactionTokenPb(t, config, finalMintTransactionHash, tokenIdentityPubKey)
	require.NoError(t, err, "failed to create first transfer transaction")

	finalTransferTransaction1, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, transaction1,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast first transfer transaction")

	finalTxHash1, err := utils.HashTokenTransaction(finalTransferTransaction1, false)
	require.NoError(t, err, "failed to hash first transfer transaction")

	entClient, err := ent.Open("postgres", config.CoordinatorDatabaseURI)
	require.NoError(t, err)
	defer entClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, t.Context(), entClient, finalTxHash1)

	transaction2, _, err := createTestTokenTransferTransactionTokenPb(t, config, finalMintTransactionHash, tokenIdentityPubKey)
	require.NoError(t, err, "failed to create second transfer transaction")

	earlierTime := time.Now().Add(-1 * time.Hour)
	transaction2.ClientCreatedTimestamp = timestamppb.New(earlierTime)

	_, _, err = wallet.StartTokenTransactionCoordinated(
		t.Context(), config, transaction2,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey}, TestValidityDurationSecs*time.Second, nil,
	)
	require.Error(t, err, "expected error when trying to pre-empt a REVEALED transaction")
	require.Contains(t, err.Error(), "cannot be spent", "error should indicate output cannot be spent")
}

func TestCoordinatedTokenTransferPreemptionPreventionFinalized(t *testing.T) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
	tokenPrivKey := config.IdentityPrivateKey
	tokenIdentityPubKey := tokenPrivKey.Public()

	mintTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenIdentityPubKey)
	require.NoError(t, err, "failed to create mint transaction for transfer test")

	finalMintTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTransaction,
		[]keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast mint transaction for transfer test")

	finalMintTransactionHash, err := utils.HashTokenTransaction(finalMintTransaction, false)
	require.NoError(t, err, "failed to hash mint transaction")

	transaction1, _, err := createTestTokenTransferTransactionTokenPb(t, config, finalMintTransactionHash, tokenIdentityPubKey)
	require.NoError(t, err, "failed to create first transfer transaction")

	_, err = wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, transaction1,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast first transfer transaction")

	transaction2, _, err := createTestTokenTransferTransactionTokenPb(t, config, finalMintTransactionHash, tokenIdentityPubKey)
	require.NoError(t, err, "failed to create second transfer transaction")

	earlierTime := time.Now().Add(-1 * time.Hour)
	transaction2.ClientCreatedTimestamp = timestamppb.New(earlierTime)

	_, _, err = wallet.StartTokenTransactionCoordinated(
		t.Context(), config, transaction2,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey}, TestValidityDurationSecs*time.Second, nil,
	)
	require.Error(t, err, "expected error when trying to pre-empt a FINALIZED transaction")
	require.Contains(t, err.Error(), "cannot be spent", "error should indicate output cannot be spent")
}
