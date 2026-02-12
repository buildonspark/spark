package tokens_test

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokenpbinternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

var internalBroadcastMethod = tokenpbinternal.SparkTokenInternalService_SignTokenTransaction_FullMethodName

func disableInternalBroadcast(t *testing.T, kc *sparktesting.KnobController) {
	t.Helper()
	err := kc.SetKnobWithTarget(t, knobs.KnobGrpcServerMethodEnabled, internalBroadcastMethod, 0)
	require.NoError(t, err)
}

func enableInternalBroadcast(t *testing.T, kc *sparktesting.KnobController) {
	t.Helper()
	err := kc.SetKnobWithTarget(t, knobs.KnobGrpcServerMethodEnabled, internalBroadcastMethod, 100)
	require.NoError(t, err)
}

func requirePartialCommit(t *testing.T, resp *tokenpb.BroadcastTransactionResponse) {
	t.Helper()
	require.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)
	require.Len(t, resp.CommitProgress.CommittedOperatorPublicKeys, 1, "only coordinator should be committed")
	require.Len(t, resp.CommitProgress.UncommittedOperatorPublicKeys, 2, "non-coordinator operators should be uncommitted")
}

func triggerRetryTask(t *testing.T, config *wallet.TestWalletConfig) {
	t.Helper()
	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{
		TaskName: "retry_signed_token_transaction_broadcasts",
	})
	require.NoError(t, err)
}

func skipIfNotPhase2(t *testing.T) {
	t.Helper()
	if !broadcastTokenTestsUsePhase2 {
		t.Skipf("Skipping %s - only runs for TTV3_Phase2", currentBroadcastRunLabel())
	}
	sparktesting.RequireMinikube(t)
}

// TestTokenMintOperatorOfflineAutoRetry tests that a mint transaction can be retried
// via the retry task when an operator comes back online before the transaction expires.
func TestTokenMintOperatorOfflineAutoRetry(t *testing.T) {
	skipIfNotPhase2(t)

	sparktesting.WithTimeout(t, 2*time.Minute, func(t *testing.T) {
		kc, err := sparktesting.NewKnobController(t)
		require.NoError(t, err)
		err = kc.SetKnob(t, knobs.KnobTokenTransactionV3Phase2RetryEnabled, 100)
		require.NoError(t, err)

		config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		tokenPrivKey := config.IdentityPrivateKey
		tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenPrivKey.Public())

		disableInternalBroadcast(t, kc)

		recipientPrivKey := keys.GeneratePrivateKey()
		mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: tokenPrivKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{500},
		})
		require.NoError(t, err)
		mintTx.TokenOutputs[0].OwnerPublicKey = recipientPrivKey.Public().Serialize()

		resp, err := wallet.BroadcastTokenTransactionV3WithResponse(
			t.Context(), config, mintTx, []keys.Private{tokenPrivKey}, wallet.DefaultValidityDuration)
		require.NoError(t, err)
		requirePartialCommit(t, resp)

		enableInternalBroadcast(t, kc)
		triggerRetryTask(t, config)

		verifyTokenBalance(t, recipientPrivKey, tokenPrivKey.Public(), 500, "mint auto-retry")
	})
}

// TestTokenTransferOperatorOfflineAutoRetry tests that a transfer transaction can be retried
// via the retry task when an operator comes back online before the transaction expires.
func TestTokenTransferOperatorOfflineAutoRetry(t *testing.T) {
	skipIfNotPhase2(t)

	sparktesting.WithTimeout(t, 2*time.Minute, func(t *testing.T) {
		kc, err := sparktesting.NewKnobController(t)
		require.NoError(t, err)
		err = kc.SetKnob(t, knobs.KnobTokenTransactionV3Phase2RetryEnabled, 100)
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

		disableInternalBroadcast(t, kc)

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

		resp, err := wallet.BroadcastTokenTransactionV3WithResponse(
			t.Context(), config, transferTx, []keys.Private{senderPrivKey}, wallet.DefaultValidityDuration)
		require.NoError(t, err)
		requirePartialCommit(t, resp)

		enableInternalBroadcast(t, kc)
		triggerRetryTask(t, config)

		verifyTokenBalance(t, recipientPrivKey, tokenPrivKey.Public(), 1000, "transfer auto-retry")
	})
}

// TestTokenTransferOperatorOfflineRetryAfterExpiry tests that a fresh transfer transaction
// succeeds after the original transaction expires and the operator comes back online.
func TestTokenTransferOperatorOfflineRetryAfterExpiry(t *testing.T) {
	skipIfNotPhase2(t)

	sparktesting.WithTimeout(t, 2*time.Minute, func(t *testing.T) {
		kc, err := sparktesting.NewKnobController(t)
		require.NoError(t, err)
		err = kc.SetKnob(t, knobs.KnobTokenTransactionV3Phase2RetryEnabled, 0)
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

		disableInternalBroadcast(t, kc)

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

		resp, err := wallet.BroadcastTokenTransactionV3WithResponse(t.Context(), config, transferTx, []keys.Private{senderPrivKey}, 5*time.Second)
		require.NoError(t, err)
		requirePartialCommit(t, resp)

		// Wait for expiry then re-enable
		time.Sleep(6 * time.Second)
		enableInternalBroadcast(t, kc)

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
