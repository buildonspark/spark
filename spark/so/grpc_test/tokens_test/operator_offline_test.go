package tokens_test

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

// TestTokenMintOperatorOfflineAutoRetry tests that a mint transaction can be retried
// via the retry task when an operator comes back online before the transaction expires.
func TestTokenMintOperatorOfflineAutoRetry(t *testing.T) {
	if !broadcastTokenTestsUsePhase2 {
		t.Skipf("Skipping %s - only runs for TTV3_Phase2", currentBroadcastRunLabel())
	}
	sparktesting.RequireMinikube(t)

	sparktesting.WithTimeout(t, 2*time.Minute, func(t *testing.T) {
		knobController, err := sparktesting.NewKnobController(t)
		require.NoError(t, err)
		err = knobController.SetKnob(t, knobs.KnobTokenTransactionV3Phase2RetryEnabled, 100)
		require.NoError(t, err)

		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		tokenPrivKey := config.IdentityPrivateKey
		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		soController, err := sparktesting.NewSparkOperatorController(t)
		require.NoError(t, err)
		err = soController.DisableOperator(t, 2)
		require.NoError(t, err)

		recipientPrivKey := keys.GeneratePrivateKey()
		mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: tokenPrivKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{500},
		})
		require.NoError(t, err)
		mintTx.TokenOutputs[0].OwnerPublicKey = recipientPrivKey.Public().Serialize()

		// Broadcast returns COMMIT_PROCESSING with partial progress when operator is offline
		resp, _ := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, mintTx, []keys.Private{tokenPrivKey}, wallet.DefaultValidityDuration)
		require.NotNil(t, resp, "response should not be nil")
		require.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus,
			"expected COMMIT_PROCESSING when operator is offline, got %s", resp.CommitStatus)
		require.NotNil(t, resp.CommitProgress, "commit progress should be set")
		require.GreaterOrEqual(t, len(resp.CommitProgress.CommittedOperatorPublicKeys), 1,
			"should have at least 1 committed operator (coordinator)")
		require.GreaterOrEqual(t, len(resp.CommitProgress.UncommittedOperatorPublicKeys), 1,
			"should have at least 1 uncommitted operator (the disabled one)")

		err = soController.EnableOperator(t, 2)
		require.NoError(t, err)

		conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
		require.NoError(t, err)
		defer conn.Close()

		mockClient := pbmock.NewMockServiceClient(conn)
		_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{
			TaskName: "retry_signed_token_transaction_broadcasts",
		})
		require.NoError(t, err)

		verifyTokenBalance(t, recipientPrivKey, tokenPrivKey.Public(), 500, "mint auto-retry")
	})
}

// TestTokenTransferOperatorOfflineAutoRetry tests that a transfer transaction can be retried
// via the retry task when an operator comes back online before the transaction expires.
func TestTokenTransferOperatorOfflineAutoRetry(t *testing.T) {
	if !broadcastTokenTestsUsePhase2 {
		t.Skipf("Skipping %s - only runs for TTV3_Phase2", currentBroadcastRunLabel())
	}
	sparktesting.RequireMinikube(t)

	sparktesting.WithTimeout(t, 2*time.Minute, func(t *testing.T) {
		knobController, err := sparktesting.NewKnobController(t)
		require.NoError(t, err)
		err = knobController.SetKnob(t, knobs.KnobTokenTransactionV3Phase2RetryEnabled, 100)
		require.NoError(t, err)

		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		tokenPrivKey := config.IdentityPrivateKey
		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		senderPrivKey := keys.GeneratePrivateKey()
		mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: tokenPrivKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{1000},
		})
		require.NoError(t, err)
		mintTx.TokenOutputs[0].OwnerPublicKey = senderPrivKey.Public().Serialize()

		finalMint, err := broadcastTokenTransaction(t, t.Context(), config, mintTx, []keys.Private{tokenPrivKey})
		require.NoError(t, err)
		mintTxHash, err := utils.HashTokenTransaction(finalMint, false)
		require.NoError(t, err)

		soController, err := sparktesting.NewSparkOperatorController(t)
		require.NoError(t, err)
		err = soController.DisableOperator(t, 2)
		require.NoError(t, err)

		recipientPrivKey := keys.GeneratePrivateKey()
		transferTx, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey:            tokenPrivKey.Public(),
			TokenIdentifier:                tokenIdentifier,
			FinalIssueTokenTransactionHash: mintTxHash,
			NumOutputsToSpend:              1,
		})
		require.NoError(t, err)
		transferTx.TokenOutputs[0].OwnerPublicKey = recipientPrivKey.Public().Serialize()
		transferTx.TokenOutputs[0].TokenAmount = int64ToUint128Bytes(0, 1000)

		// Broadcast returns COMMIT_PROCESSING with partial progress when operator is offline
		resp, _ := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, transferTx, []keys.Private{senderPrivKey}, wallet.DefaultValidityDuration)
		require.NotNil(t, resp, "response should not be nil")
		require.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus,
			"expected COMMIT_PROCESSING when operator is offline, got %s", resp.CommitStatus)
		require.NotNil(t, resp.CommitProgress, "commit progress should be set")
		require.GreaterOrEqual(t, len(resp.CommitProgress.CommittedOperatorPublicKeys), 1,
			"should have at least 1 committed operator (coordinator)")
		require.GreaterOrEqual(t, len(resp.CommitProgress.UncommittedOperatorPublicKeys), 1,
			"should have at least 1 uncommitted operator (the disabled one)")

		err = soController.EnableOperator(t, 2)
		require.NoError(t, err)

		conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
		require.NoError(t, err)
		defer conn.Close()

		mockClient := pbmock.NewMockServiceClient(conn)
		_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{
			TaskName: "retry_signed_token_transaction_broadcasts",
		})
		require.NoError(t, err)

		verifyTokenBalance(t, recipientPrivKey, tokenPrivKey.Public(), 1000, "transfer auto-retry")
	})
}

// TestTokenTransferOperatorOfflineRetryAfterExpiry tests that a fresh transfer transaction
// succeeds after the original transaction expires and the operator comes back online.
func TestTokenTransferOperatorOfflineRetryAfterExpiry(t *testing.T) {
	if !broadcastTokenTestsUsePhase2 {
		t.Skipf("Skipping %s - only runs for TTV3_Phase2", currentBroadcastRunLabel())
	}
	sparktesting.RequireMinikube(t)

	sparktesting.WithTimeout(t, 2*time.Minute, func(t *testing.T) {
		knobController, err := sparktesting.NewKnobController(t)
		require.NoError(t, err)
		err = knobController.SetKnob(t, knobs.KnobTokenTransactionV3Phase2RetryEnabled, 0)
		require.NoError(t, err)

		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		tokenPrivKey := config.IdentityPrivateKey
		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		senderPrivKey := keys.GeneratePrivateKey()
		mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: tokenPrivKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{1000},
		})
		require.NoError(t, err)
		mintTx.TokenOutputs[0].OwnerPublicKey = senderPrivKey.Public().Serialize()

		finalMint, err := broadcastTokenTransaction(t, t.Context(), config, mintTx, []keys.Private{tokenPrivKey})
		require.NoError(t, err)
		mintTxHash, err := utils.HashTokenTransaction(finalMint, false)
		require.NoError(t, err)

		soController, err := sparktesting.NewSparkOperatorController(t)
		require.NoError(t, err)
		err = soController.DisableOperator(t, 2)
		require.NoError(t, err)

		recipientPrivKey := keys.GeneratePrivateKey()
		transferTx, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey:            tokenPrivKey.Public(),
			TokenIdentifier:                tokenIdentifier,
			FinalIssueTokenTransactionHash: mintTxHash,
			NumOutputsToSpend:              1,
		})
		require.NoError(t, err)
		transferTx.TokenOutputs[0].OwnerPublicKey = recipientPrivKey.Public().Serialize()
		transferTx.TokenOutputs[0].TokenAmount = int64ToUint128Bytes(0, 1000)

		// Broadcast with short validity returns COMMIT_PROCESSING with partial progress
		resp, _ := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, transferTx, []keys.Private{senderPrivKey}, 5*time.Second)
		require.NotNil(t, resp, "response should not be nil")
		require.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus,
			"expected COMMIT_PROCESSING when operator is offline, got %s", resp.CommitStatus)
		require.NotNil(t, resp.CommitProgress, "commit progress should be set")
		require.GreaterOrEqual(t, len(resp.CommitProgress.CommittedOperatorPublicKeys), 1,
			"should have at least 1 committed operator (coordinator)")
		require.GreaterOrEqual(t, len(resp.CommitProgress.UncommittedOperatorPublicKeys), 1,
			"should have at least 1 uncommitted operator (the disabled one)")

		// Wait for expiry then re-enable operator
		time.Sleep(6 * time.Second)
		err = soController.EnableOperator(t, 2)
		require.NoError(t, err)

		// Fresh transfer should succeed after expiry
		transferTx2, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey:            tokenPrivKey.Public(),
			TokenIdentifier:                tokenIdentifier,
			FinalIssueTokenTransactionHash: mintTxHash,
			NumOutputsToSpend:              1,
		})
		require.NoError(t, err)
		transferTx2.TokenOutputs[0].OwnerPublicKey = recipientPrivKey.Public().Serialize()
		transferTx2.TokenOutputs[0].TokenAmount = int64ToUint128Bytes(0, 1000)

		_, err = broadcastTokenTransaction(t, t.Context(), config, transferTx2, []keys.Private{senderPrivKey})
		require.NoError(t, err, "fresh transfer should succeed")

		verifyTokenBalance(t, recipientPrivKey, tokenPrivKey.Public(), 1000, "transfer after expiry")
	})
}
