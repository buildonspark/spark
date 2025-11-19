package tokens_test

import (
	"math/big"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

func TestFreezeAndUnfreezeTokens(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())
			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err, "failed to create test token issuance transaction")

			finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			for i, output := range finalIssueTokenTransaction.TokenOutputs {
				if output.GetWithdrawBondSats() != withdrawalBondSatsInConfig {
					t.Errorf("output %d: expected withdrawal bond sats %d, got %d", i, uint64(withdrawalBondSatsInConfig), output.GetWithdrawBondSats())
				}
				if output.GetWithdrawRelativeBlockLocktime() != uint64(withdrawalRelativeBlockLocktimeInConfig) {
					t.Errorf("output %d: expected withdrawal relative block locktime %d, got %d", i, uint64(withdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime())
				}
			}

			ownerPubKey, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].OwnerPublicKey)
			require.NoError(t, err)
			freezeResponse, err := wallet.FreezeTokens(t.Context(), config, ownerPubKey, finalIssueTokenTransaction.TokenOutputs[0].TokenIdentifier, false)
			require.NoError(t, err, "failed to freeze tokens")

			frozenAmount := new(big.Int).SetBytes(freezeResponse.ImpactedTokenAmount)

			expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, testIssueOutput1Amount))
			expectedOutputID := finalIssueTokenTransaction.TokenOutputs[0].Id

			require.Equal(t, expectedAmount, frozenAmount,
				"frozen amount %s does not match expected amount %s", frozenAmount.String(), expectedAmount.String())
			require.Len(t, freezeResponse.ImpactedOutputIds, 1, "expected 1 impacted output ID")
			require.Equal(t, *expectedOutputID, freezeResponse.ImpactedOutputIds[0],
				"frozen output ID %s does not match expected output ID %s", freezeResponse.ImpactedOutputIds[0], *expectedOutputID)

			finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
			require.NoError(t, err, "failed to hash final transfer token transaction")

			transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPb(
				t, config, finalIssueTokenTransactionHash, tokenPrivKey.Public(), tokenIdentifier,
			)
			require.NoError(t, err, "failed to create test token transfer transaction")

			transferFrozenTokenTransactionResponse, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.Error(t, err, "expected error when transferring frozen tokens")
			require.Nil(t, transferFrozenTokenTransactionResponse, "expected nil response when transferring frozen tokens")

			unfreezeResponse, err := wallet.FreezeTokens(t.Context(), config, ownerPubKey, finalIssueTokenTransaction.TokenOutputs[0].TokenIdentifier, true)
			require.NoError(t, err, "failed to unfreeze tokens")

			thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount)

			require.Equal(t, expectedAmount, thawedAmount,
				"thawed amount %s does not match expected amount %s", thawedAmount.String(), expectedAmount.String())
			require.Len(t, unfreezeResponse.ImpactedOutputIds, 1, "expected 1 impacted output ID")
			require.Equal(t, *expectedOutputID, unfreezeResponse.ImpactedOutputIds[0],
				"thawed output ID %s does not match expected output ID %s", unfreezeResponse.ImpactedOutputIds[0], *expectedOutputID)

			transferTokenTransactionResponse, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast thawed token transaction")
			require.NotNil(t, transferTokenTransactionResponse, "expected non-nil response when transferring thawed tokens")
		})
	}
}
