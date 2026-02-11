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

			finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
			require.NoError(t, err, "failed to hash final transfer token transaction")

			require.Equal(t, expectedAmount, frozenAmount,
				"frozen amount %s does not match expected amount %s", frozenAmount.String(), expectedAmount.String())
			require.Len(t, freezeResponse.ImpactedTokenOutputs, 1, "expected 1 impacted token output")
			require.Equal(t, finalIssueTokenTransactionHash, freezeResponse.ImpactedTokenOutputs[0].TransactionHash,
				"freeze response transaction hash mismatch")
			require.Equal(t, uint32(0), freezeResponse.ImpactedTokenOutputs[0].Vout,
				"freeze response vout mismatch")

			transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPb(
				t, config, finalIssueTokenTransactionHash, tokenPrivKey.Public(), tokenIdentifier,
			)
			require.NoError(t, err, "failed to create test token transfer transaction")

			transferFrozenTokenTransactionResponse, err := broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.Error(t, err, "expected error when transferring frozen tokens")
			require.Nil(t, transferFrozenTokenTransactionResponse, "expected nil response when transferring frozen tokens")

			unfreezeResponse, err := wallet.FreezeTokens(t.Context(), config, ownerPubKey, finalIssueTokenTransaction.TokenOutputs[0].TokenIdentifier, true)
			require.NoError(t, err, "failed to unfreeze tokens")

			thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount)

			require.Equal(t, expectedAmount, thawedAmount,
				"thawed amount %s does not match expected amount %s", thawedAmount.String(), expectedAmount.String())
			require.Len(t, unfreezeResponse.ImpactedTokenOutputs, 1, "expected 1 impacted token output")
			require.Equal(t, finalIssueTokenTransactionHash, unfreezeResponse.ImpactedTokenOutputs[0].TransactionHash,
				"unfreeze response transaction hash mismatch")
			require.Equal(t, uint32(0), unfreezeResponse.ImpactedTokenOutputs[0].Vout,
				"unfreeze response vout mismatch")

			transferTokenTransactionPostThaw, _, err := createTestTokenTransferTransactionTokenPb(
				t, config, finalIssueTokenTransactionHash, tokenPrivKey.Public(), tokenIdentifier,
			)
			require.NoError(t, err, "failed to create test token transfer transaction after thaw")

			transferTokenTransactionResponse, err := broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				transferTokenTransactionPostThaw,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast thawed token transaction")
			require.NotNil(t, transferTokenTransactionResponse, "expected non-nil response when transferring thawed tokens")
		})
	}
}

func TestGlobalPauseBlocksTransferAndMint(t *testing.T) {
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
			require.NoError(t, err)

			pauseResp, err := wallet.GlobalPauseTokens(t.Context(), config, tokenIdentifier, false)
			require.NoError(t, err, "failed to global pause token")
			require.NotNil(t, pauseResp)

			transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPb(
				t, config, finalIssueTokenTransactionHash, tokenPrivKey.Public(), tokenIdentifier,
			)
			require.NoError(t, err)

			transferResp, err := broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.Error(t, err, "expected error when transferring globally paused tokens")
			require.Nil(t, transferResp)

			mintTx, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err)

			mintResp, err := broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				mintTx,
				[]keys.Private{tokenPrivKey},
			)
			require.Error(t, err, "expected error when minting globally paused tokens")
			require.Nil(t, mintResp)

			unpauseResp, err := wallet.GlobalPauseTokens(t.Context(), config, tokenIdentifier, true)
			require.NoError(t, err, "failed to unpause token")
			require.NotNil(t, unpauseResp)

			// Transfer should now succeed
			transferTokenTransactionPostUnpause, _, err := createTestTokenTransferTransactionTokenPb(
				t, config, finalIssueTokenTransactionHash, tokenPrivKey.Public(), tokenIdentifier,
			)
			require.NoError(t, err)

			transferRespPostUnpause, err := broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				transferTokenTransactionPostUnpause,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to transfer after unpause")
			require.NotNil(t, transferRespPostUnpause)

			mintTxPostUnpause, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public(), tokenIdentifier)
			require.NoError(t, err)

			mintRespPostUnpause, err := broadcastTokenTransaction(
				t,
				t.Context(),
				config,
				mintTxPostUnpause,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to mint after unpause")
			require.NotNil(t, mintRespPostUnpause)
		})
	}
}

func TestFreezeBlocksMultiTokenTransfer(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set up first token with one output to owner A
			configA := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			configA.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures
			tokenPrivKeyA := configA.IdentityPrivateKey
			tokenIdentifierA := queryTokenIdentifierOrFail(t, configA, tokenPrivKeyA.Public())
			mintTxABefore, ownerAPrivs, err := createTestTokenMintTransactionTokenPbWithParams(t, configA, tokenTransactionParams{
				TokenIdentityPubKey: tokenPrivKeyA.Public(),
				TokenIdentifier:     tokenIdentifierA,
				NumOutputs:          1,
				OutputAmounts:       []uint64{uint64(testIssueOutput1Amount)},
			})
			require.NoError(t, err, "failed to create mint A")
			require.Len(t, ownerAPrivs, 1)
			finalMintATx, err := broadcastTokenTransaction(
				t, t.Context(), configA, mintTxABefore, []keys.Private{tokenPrivKeyA})
			require.NoError(t, err, "failed to broadcast mint A")
			mintATxHash, err := utils.HashTokenTransaction(finalMintATx, false)
			require.NoError(t, err, "failed to hash mint A")

			// Set up second token with one output to owner B (different token)
			issuerB := keys.GeneratePrivateKey()
			configB := wallet.NewTestWalletConfigWithIdentityKey(t, issuerB)
			configB.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures
			err = testCreateNativeSparkTokenWithParams(t, configB, sparkTokenCreationTestParams{
				issuerPrivateKey: issuerB,
				name:             "Freeze MultiToken",
				ticker:           "FMT",
				maxSupply:        1_000_000,
			})
			require.NoError(t, err, "failed to create second token")
			tokenIdentifierB := queryTokenIdentifierOrFail(t, configB, issuerB.Public())
			mintTxBBefore, ownerBPrivs, err := createTestTokenMintTransactionTokenPbWithParams(t, configB, tokenTransactionParams{
				TokenIdentityPubKey: issuerB.Public(),
				TokenIdentifier:     tokenIdentifierB,
				NumOutputs:          1,
				OutputAmounts:       []uint64{uint64(testIssueOutput1Amount)},
			})
			require.NoError(t, err, "failed to create mint B")
			require.Len(t, ownerBPrivs, 1)
			finalMintBTx, err := broadcastTokenTransaction(
				t, t.Context(), configB, mintTxBBefore, []keys.Private{issuerB})
			require.NoError(t, err, "failed to broadcast mint B")
			mintBTxHash, err := utils.HashTokenTransaction(finalMintBTx, false)
			require.NoError(t, err, "failed to hash mint B")

			// Freeze owner A's tokens for token A
			ownerAPub := ownerAPrivs[0].Public()
			_, err = wallet.FreezeTokens(t.Context(), configA, ownerAPub, tokenIdentifierA, false)
			require.NoError(t, err, "failed to freeze token A for owner A")

			// Attempt a multi-token transfer spending one input from each token
			recipient := keys.GeneratePrivateKey()
			transferTx := createTestMultiTokenTransferTransactionTokenPb(
				t,
				configA,
				mintATxHash,
				tokenIdentifierA,
				mintBTxHash,
				tokenIdentifierB,
				recipient.Public(),
			)

			// Should fail due to active freeze on owner A + token A, even with mixed tokens in one transfer
			resp, err := broadcastTokenTransaction(
				t,
				t.Context(),
				configA,
				transferTx,
				[]keys.Private{ownerAPrivs[0], ownerBPrivs[0]},
			)
			require.Error(t, err, "expected error when transferring with a frozen input among multiple tokens")
			require.Nil(t, resp, "expected nil response when transfer blocked by freeze")
		})
	}
}
