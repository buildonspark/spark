package tokens_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokenpartialrevocationsecretshare"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/tokentransactionpeersignature"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRevocationExchangeCronJobSuccessfullyFinalizesRevealed(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, _, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err, "failed to create transfer token transaction")

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, entClient, finalTransferTokenTransactionHash)

	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
	// The cron job might return an error if there are other unrelated failing transactions in the DB.
	// We verify success by checking the DB state below.
	if err != nil {
		t.Logf("TriggerTask returned error (ignoring if unrelated): %v", err)
	}
	conn.Close()

	tokenTransactionAfterFinalizeRevealedTransactions, err := entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TokenTransactionStatusFinalized, tokenTransactionAfterFinalizeRevealedTransactions.Status)
	for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput {
		require.Equal(t, len(tokenOutput.Edges.TokenPartialRevocationSecretShares), len(config.SigningOperators)-1, "should have exactly numOperators-1 secret shares")
		require.Equal(t, st.TokenOutputStatusSpentFinalized, tokenOutput.Status)
	}
	for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.CreatedOutput {
		require.Equal(t, st.TokenOutputStatusCreatedFinalized, tokenOutput.Status)
	}
}

func TestRevocationExchangeCronJobSuccessfullyFinalizesRemappedOutputsAvailableToSpend(t *testing.T) {
	testCases := []struct {
		name                          string
		nonCoordinatorInitialTxStatus st.TokenTransactionStatus
		nonCoordinatorRemapTxStatus   st.TokenTransactionStatus
	}{
		{
			name:                          "Successfully finalizes REVEALED/SIGNED/FINALIZED when the remapped transaction on the non-coordinator is SIGNED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusSigned,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusSigned,
		},
		{
			name:                          "Successfully finalizes REVEALED/SIGNED/FINALIZED when the remapped transaction on the non-coordinator is STARTED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusSigned,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusStarted,
		},
		{
			name:                          "Successfully finalizes REVEALED/STARTED/FINALIZED when the remapped transaction on the non-coordinator is SIGNED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusStarted,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusSigned,
		},
		{
			name:                          "Successfully finalizes REVEALED/STARTED/FINALIZED when the remapped transaction on the non-coordinator is STARTED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusStarted,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusStarted,
		},
	}

	preemptLoserTimestamp := utils.ToMicrosecondPrecision(time.Now().UTC())
	preemptWinnerTimestamp := preemptLoserTimestamp.Add(-2 * time.Minute)

	for _, tc := range testCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			ctx := t.Context()

			config, initalTransferTokenTransactionHash, _, err := createTransferTokenTransactionForWalletWithTimestamp(t, ctx, preemptWinnerTimestamp)
			require.NoError(t, err, "failed to create transfer token transaction")

			coordinatorEntClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
			defer coordinatorEntClient.Close()

			nonCoordOperatorConfig := sparktesting.SpecificOperatorTestConfig(t, 1)
			nonCoordEntClient := db.NewPostgresEntClientForIntegrationTest(t, nonCoordOperatorConfig.DatabasePath)
			defer nonCoordEntClient.Close()

			setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, coordinatorEntClient, initalTransferTokenTransactionHash)

			setAndValidateSuccessfulTokenTransactionToStatusAndRemapSpentOutputsForOperator(
				t, ctx,
				config,
				nonCoordOperatorConfig,
				coordinatorEntClient,
				nonCoordEntClient,
				initalTransferTokenTransactionHash,
				tc.nonCoordinatorInitialTxStatus,
				tc.nonCoordinatorRemapTxStatus,
				true,
				preemptLoserTimestamp,
			)

			conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
			require.NoError(t, err)
			mockClient := pbmock.NewMockServiceClient(conn)
			_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
			require.NoError(t, err)
			conn.Close()

			tokenTransactionAfterFinalizeRevealedTransactions, err := coordinatorEntClient.TokenTransaction.Query().
				Where(tokentransaction.FinalizedTokenTransactionHashEQ(initalTransferTokenTransactionHash)).
				WithPeerSignatures().
				WithSpentOutput(func(to *ent.TokenOutputQuery) { to.WithTokenPartialRevocationSecretShares() }).
				WithCreatedOutput().
				Only(ctx)
			require.NoError(t, err)
			require.Equal(t, st.TokenTransactionStatusFinalized, tokenTransactionAfterFinalizeRevealedTransactions.Status)

			nonCoordinatorTokenTransaction, err := nonCoordEntClient.TokenTransaction.Query().
				Where(tokentransaction.FinalizedTokenTransactionHashEQ(initalTransferTokenTransactionHash)).
				WithSpentOutput().
				WithCreatedOutput().
				Only(ctx)

			for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput {
				require.Equal(t, len(tokenOutput.Edges.TokenPartialRevocationSecretShares), len(config.SigningOperators)-1, "should have exactly numOperators-1 secret shares")
				require.Equal(t, st.TokenOutputStatusSpentFinalized, tokenOutput.Status)
			}
			for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.CreatedOutput {
				require.Equal(t, st.TokenOutputStatusCreatedFinalized, tokenOutput.Status)
			}

			require.NoError(t, err)
			require.Equal(t, st.TokenTransactionStatusFinalized, nonCoordinatorTokenTransaction.Status)
			require.Len(t, nonCoordinatorTokenTransaction.Edges.SpentOutput, len(tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput), "should have the same number of spent outputs")
			for _, tokenOutput := range nonCoordinatorTokenTransaction.Edges.SpentOutput {
				require.Equal(t, st.TokenOutputStatusSpentFinalized, tokenOutput.Status)
			}
			require.Len(t, nonCoordinatorTokenTransaction.Edges.CreatedOutput, len(tokenTransactionAfterFinalizeRevealedTransactions.Edges.CreatedOutput), "should have the same number of created outputs")
			for _, tokenOutput := range nonCoordinatorTokenTransaction.Edges.CreatedOutput {
				require.Equal(t, st.TokenOutputStatusCreatedFinalized, tokenOutput.Status)
			}
		})
	}
}

// REVEALED txA has its outputs remapped to a new tx, txB on a different operator.
// txB is not yet expired - fallback to preemption check.
// If txA wins the preemption check vs txB, finalize successfully.
func TestRevocationExchangeCronJobSuccessfullyFinalizesRemappedOutputsIfRemapTxHasNotExpiredAndInitialTxNotPreemptedByRemapTx(t *testing.T) {
	testCases := []struct {
		name                          string
		nonCoordinatorInitialTxStatus st.TokenTransactionStatus
		nonCoordinatorRemapTxStatus   st.TokenTransactionStatus
	}{
		{
			name:                          "Successfully finalizes REVEALED/SIGNED/FINALIZED when the remapped transaction on the non-coordinator is SIGNED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusSigned,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusSigned,
		},
		{
			name:                          "Successfully finalizes REVEALED/SIGNED/FINALIZED when the remapped transaction on the non-coordinator is STARTED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusSigned,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusStarted,
		},
		{
			name:                          "Successfully finalizes REVEALED/STARTED/FINALIZED when the remapped transaction on the non-coordinator is SIGNED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusStarted,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusSigned,
		},
		{
			name:                          "Successfully finalizes REVEALED/STARTED/FINALIZED when the remapped transaction on the non-coordinator is STARTED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusStarted,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusStarted,
		},
	}

	preemptLoserTimestamp := utils.ToMicrosecondPrecision(time.Now().UTC())
	preemptWinnerTimestamp := preemptLoserTimestamp.Add(-2 * time.Minute)

	for _, tc := range testCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			ctx := t.Context()

			config, initalTransferTokenTransactionHash, _, err := createTransferTokenTransactionForWalletWithTimestamp(t, ctx, preemptWinnerTimestamp)
			require.NoError(t, err, "failed to create transfer token transaction")

			coordinatorEntClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
			defer coordinatorEntClient.Close()

			nonCoordOperatorConfig := sparktesting.SpecificOperatorTestConfig(t, 1)
			nonCoordEntClient := db.NewPostgresEntClientForIntegrationTest(t, nonCoordOperatorConfig.DatabasePath)
			defer nonCoordEntClient.Close()

			setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, coordinatorEntClient, initalTransferTokenTransactionHash)

			setAndValidateSuccessfulTokenTransactionToStatusAndRemapSpentOutputsForOperator(
				t, ctx,
				config,
				nonCoordOperatorConfig,
				coordinatorEntClient,
				nonCoordEntClient,
				initalTransferTokenTransactionHash,
				tc.nonCoordinatorInitialTxStatus,
				tc.nonCoordinatorRemapTxStatus,
				false,
				preemptLoserTimestamp,
			)

			conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
			require.NoError(t, err)
			mockClient := pbmock.NewMockServiceClient(conn)
			_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
			require.NoError(t, err)
			conn.Close()

			tokenTransactionAfterFinalizeRevealedTransactions, err := coordinatorEntClient.TokenTransaction.Query().
				Where(tokentransaction.FinalizedTokenTransactionHashEQ(initalTransferTokenTransactionHash)).
				WithPeerSignatures().
				WithSpentOutput(func(to *ent.TokenOutputQuery) { to.WithTokenPartialRevocationSecretShares() }).
				WithCreatedOutput().
				Only(ctx)
			require.NoError(t, err)
			require.Equal(t, st.TokenTransactionStatusFinalized, tokenTransactionAfterFinalizeRevealedTransactions.Status)

			nonCoordinatorTokenTransaction, err := nonCoordEntClient.TokenTransaction.Query().
				Where(tokentransaction.FinalizedTokenTransactionHashEQ(initalTransferTokenTransactionHash)).
				WithSpentOutput().
				WithCreatedOutput().
				Only(ctx)

			for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput {
				require.Equal(t, len(tokenOutput.Edges.TokenPartialRevocationSecretShares), len(config.SigningOperators)-1, "should have exactly numOperators-1 secret shares")
				require.Equal(t, st.TokenOutputStatusSpentFinalized, tokenOutput.Status)
			}
			for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.CreatedOutput {
				require.Equal(t, st.TokenOutputStatusCreatedFinalized, tokenOutput.Status)
			}

			require.NoError(t, err)
			require.Equal(t, st.TokenTransactionStatusFinalized, nonCoordinatorTokenTransaction.Status)
			require.Len(t, nonCoordinatorTokenTransaction.Edges.SpentOutput, len(tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput), "should have the same number of spent outputs")
			for _, tokenOutput := range nonCoordinatorTokenTransaction.Edges.SpentOutput {
				require.Equal(t, st.TokenOutputStatusSpentFinalized, tokenOutput.Status)
			}
			require.Len(t, nonCoordinatorTokenTransaction.Edges.CreatedOutput, len(tokenTransactionAfterFinalizeRevealedTransactions.Edges.CreatedOutput), "should have the same number of created outputs")
			for _, tokenOutput := range nonCoordinatorTokenTransaction.Edges.CreatedOutput {
				require.Equal(t, st.TokenOutputStatusCreatedFinalized, tokenOutput.Status)
			}
		})
	}
}

// REVEALED txA has its outputs remapped to a new tx, txB on a different operator.
// txB is not yet expired - fallback to preemption check.
// If txA loses the preemption check vs txB, fail to finalize.
func TestRevocationExchangeCronJobFailsToReclaimOutputsIfRemappedTransactionHasNotExpiredAndIsPreemptedByRemapTx(t *testing.T) {
	testCases := []struct {
		name                          string
		nonCoordinatorInitialTxStatus st.TokenTransactionStatus
		nonCoordinatorRemapTxStatus   st.TokenTransactionStatus
	}{
		{
			name:                          "Fails to finalize REVEALED/SIGNED/FINALIZED when the remapped transaction on the non-coordinator is SIGNED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusSigned,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusSigned,
		},
		{
			name:                          "Fails to finalize REVEALED/SIGNED/FINALIZED when the remapped transaction on the non-coordinator is SIGNED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusSigned,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusStarted,
		},
		{
			name:                          "Fails to finalize REVEALED/STARTED/FINALIZED when the remapped transaction on the non-coordinator is SIGNED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusStarted,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusSigned,
		},
		{
			name:                          "Fails to finalize REVEALED/STARTED/FINALIZED when the remapped transaction on the non-coordinator is STARTED",
			nonCoordinatorInitialTxStatus: st.TokenTransactionStatusStarted,
			nonCoordinatorRemapTxStatus:   st.TokenTransactionStatusStarted,
		},
	}
	preemptLoserTimestamp := utils.ToMicrosecondPrecision(time.Now().UTC())
	preemptWinnerTimestamp := preemptLoserTimestamp.Add(-2 * time.Minute)

	for _, tc := range testCases {
		t.Run(tc.name+" ["+currentBroadcastRunLabel()+"]", func(t *testing.T) {
			ctx := t.Context()

			config, initalFinalTokenTransactionHash, _, err := createTransferTokenTransactionForWalletWithTimestamp(t, ctx, preemptLoserTimestamp)
			require.NoError(t, err, "failed to create transfer token transaction")

			coordinatorEntClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
			defer coordinatorEntClient.Close()

			nonCoordOperatorConfig := sparktesting.SpecificOperatorTestConfig(t, 1)
			nonCoordEntClient := db.NewPostgresEntClientForIntegrationTest(t, nonCoordOperatorConfig.DatabasePath)
			defer nonCoordEntClient.Close()

			setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, coordinatorEntClient, initalFinalTokenTransactionHash)

			setAndValidateSuccessfulTokenTransactionToStatusAndRemapSpentOutputsForOperator(t, ctx,
				config,
				nonCoordOperatorConfig,
				coordinatorEntClient,
				nonCoordEntClient,
				initalFinalTokenTransactionHash,
				tc.nonCoordinatorInitialTxStatus,
				tc.nonCoordinatorRemapTxStatus,
				false,
				preemptWinnerTimestamp,
			)

			conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
			require.NoError(t, err)
			mockClient := pbmock.NewMockServiceClient(conn)
			_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
			require.Error(t, err)
			conn.Close()

			coordinatorInitialTransaction, err := coordinatorEntClient.TokenTransaction.Query().
				Where(tokentransaction.FinalizedTokenTransactionHashEQ(initalFinalTokenTransactionHash)).
				Only(ctx)
			require.NoError(t, err)
			require.Equal(t, st.TokenTransactionStatusRevealed, coordinatorInitialTransaction.Status)

			nonCoordinatorInitialTransaction, err := nonCoordEntClient.TokenTransaction.Query().
				Where(tokentransaction.FinalizedTokenTransactionHashEQ(initalFinalTokenTransactionHash)).
				WithSpentOutput().
				Only(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.nonCoordinatorInitialTxStatus, nonCoordinatorInitialTransaction.Status)

			for _, tokenOutput := range coordinatorInitialTransaction.Edges.SpentOutput {
				require.Equal(t, st.TokenOutputStatusSpentFinalized, tokenOutput.Status)
			}
			require.Empty(t, nonCoordinatorInitialTransaction.Edges.SpentOutput, "should have no spent outputs")
		})
	}
}

func TestRevocationExchangeCronJobSuccessfullyFinalizesRevealedWithAllFieldsButStatusRevealed(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, _, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err, "failed to create transfer token transaction")

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedWithoutDeletingRevocationSecretShares(t, ctx, entClient, finalTransferTokenTransactionHash)

	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
	// The cron job might return an error if there are other unrelated failing transactions in the DB.
	// We verify success by checking the DB state below.
	if err != nil {
		t.Logf("TriggerTask returned error (ignoring if unrelated): %v", err)
	}
	conn.Close()

	tokenTransactionAfterFinalizeRevealedTransactions, err := entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TokenTransactionStatusFinalized, tokenTransactionAfterFinalizeRevealedTransactions.Status)
	for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput {
		require.Equal(t, len(tokenOutput.Edges.TokenPartialRevocationSecretShares), len(config.SigningOperators)-1, "should have exactly numOperators-1 secret shares")
	}
}

func TestRevocationExchangeCronJobSuccessfullyFinalizesStarted(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, _, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err, "failed to create transfer token transaction")

	var coordinatorEntClient, nonCoordEntClient *ent.Client
	coordinatorEntClient = db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer coordinatorEntClient.Close()

	nonCoordOperatorConfig := sparktesting.SpecificOperatorTestConfig(t, 1)
	nonCoordEntClient = db.NewPostgresEntClientForIntegrationTest(t, nonCoordOperatorConfig.DatabasePath)
	defer nonCoordEntClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, nonCoordEntClient, finalTransferTokenTransactionHash)
	setAndValidateSuccessfulTokenTransactionToStartedForOperator(t, ctx, coordinatorEntClient, finalTransferTokenTransactionHash)

	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000002"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
	// The cron job might return an error if there are other unrelated failing transactions in the DB.
	// We verify success by checking the DB state below.
	if err != nil {
		t.Logf("TriggerTask returned error (ignoring if unrelated): %v", err)
	}
	conn.Close()

	tokenTransactionAfterFinalizeRevealedTransactions, err := coordinatorEntClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TokenTransactionStatusFinalized, tokenTransactionAfterFinalizeRevealedTransactions.Status)
	for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput {
		require.Equal(t, len(tokenOutput.Edges.TokenPartialRevocationSecretShares), len(config.SigningOperators)-1, "should have exactly numOperators-1 secret shares")
	}
}

func TestRevocationExchangeCronJobDoesNotFinalizeStartedIfSignatureIsInvalid(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, _, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err, "failed to create transfer token transaction")

	var coordinatorEntClient, nonCoordEntClient *ent.Client
	coordinatorEntClient = db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer coordinatorEntClient.Close()

	nonCoordOperatorConfig := sparktesting.SpecificOperatorTestConfig(t, 1)
	nonCoordEntClient = db.NewPostgresEntClientForIntegrationTest(t, nonCoordOperatorConfig.DatabasePath)
	defer nonCoordEntClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, nonCoordEntClient, finalTransferTokenTransactionHash)
	peerSignature, err := nonCoordEntClient.TokenTransactionPeerSignature.Query().
		Where(tokentransactionpeersignature.And(
			tokentransactionpeersignature.HasTokenTransactionWith(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)),
			tokentransactionpeersignature.OperatorIdentityPublicKeyEQ(config.SigningOperators[config.CoordinatorIdentifier].IdentityPublicKey),
		)).Only(ctx)
	require.NoError(t, err)

	defer func() {
		err = nonCoordEntClient.TokenTransactionPeerSignature.Update().
			Where(tokentransactionpeersignature.IDEQ(peerSignature.ID)).
			SetSignature(peerSignature.Signature).
			Exec(ctx)
		require.NoError(t, err, "failed to reset peer signature; other finalize_revealed_token_transactions task tests will likely fail")
	}()

	err = nonCoordEntClient.TokenTransactionPeerSignature.Update().
		Where(tokentransactionpeersignature.IDEQ(peerSignature.ID)).
		SetSignature(make([]byte, 64)).
		Exec(ctx)
	require.NoError(t, err)
	setAndValidateSuccessfulTokenTransactionToStartedForOperator(t, ctx, coordinatorEntClient, finalTransferTokenTransactionHash)

	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000002"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
	require.Error(t, err, "should have error because signature is invalid")
	require.Contains(t, err.Error(), "failed to verify operator signatures")
	conn.Close()

	tokenTransactionAfterFinalizeRevealedTransactions, err := coordinatorEntClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TokenTransactionStatusStarted, tokenTransactionAfterFinalizeRevealedTransactions.Status)
	for _, tokenOutput := range tokenTransactionAfterFinalizeRevealedTransactions.Edges.SpentOutput {
		require.Empty(t, tokenOutput.Edges.TokenPartialRevocationSecretShares, "should have no secret shares")
	}
}

func TestRevocationExchangeCronJobSkipsRevealedWithNoSpentOutputs(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, _, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err, "failed to create transfer token transaction")

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, entClient, finalTransferTokenTransactionHash)

	tokenTransaction, err := entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithSpentOutput().
		Only(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tokenTransaction.Edges.SpentOutput)

	id := tokenTransaction.ID

	require.NoError(t,
		entClient.TokenTransaction.
			UpdateOneID(id).
			ClearSpentOutput().
			SetUpdateTime(time.Now().Add(-25*time.Minute).UTC()).
			Exec(ctx),
	)

	exists, err := entClient.TokenTransaction.
		Query().
		Where(tokentransaction.ID(id), tokentransaction.HasSpentOutput()).
		Exist(ctx)
	require.NoError(t, err)
	require.False(t, exists)

	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
	// The cron job might return an error if there are other unrelated failing transactions in the DB.
	// We verify success by checking the DB state below.
	if err != nil {
		t.Logf("TriggerTask returned error (ignoring if unrelated): %v", err)
	}
	conn.Close()

	tokenTransactionAfterFinalizeRevealedTransactions, err := entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TokenTransactionStatusRevealed, tokenTransactionAfterFinalizeRevealedTransactions.Status)
}

func TestJustInTimeFinalizationOfCreatedSignedOutputOnNonCoordinator(t *testing.T) {
	ctx := t.Context()
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
	tokenIdentityPubKey := config.IdentityPrivateKey.Public()

	// Mint a token to the issuer
	tokenIdentifier := queryTokenIdentifierOrFail(t, config, tokenIdentityPubKey)
	mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: tokenIdentityPubKey,
		TokenIdentifier:     tokenIdentifier,
		NumOutputs:          2,
		OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
		MintToSelf:          true,
	})
	require.NoError(t, err, "failed to create test token mint transaction")
	mintTxResponse, err := wallet.BroadcastTokenTransfer(
		ctx, config, mintTx,
		[]keys.Private{config.IdentityPrivateKey},
	)
	require.NoError(t, err, "failed to broadcast mint transaction")

	mintTxHash, err := utils.HashTokenTransaction(mintTxResponse, false)
	require.NoError(t, err, "failed to hash mint transaction")

	// Issuer transfers the output to Alice
	transferToAlice, alicePrivKey, err := createTestTokenTransferTransactionTokenPb(t,
		config,
		mintTxHash,
		tokenIdentityPubKey,
		tokenIdentifier,
	)
	require.NoError(t, err, "failed to create test token transfer transaction")
	transferToAliceResponse, err := wallet.BroadcastTokenTransfer(
		ctx, config, transferToAlice,
		[]keys.Private{config.IdentityPrivateKey, config.IdentityPrivateKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	transferToAliceHash, err := utils.HashTokenTransaction(transferToAliceResponse, false)
	require.NoError(t, err, "failed to hash transfer token transaction")

	nonCoordOperatorConfig := sparktesting.SpecificOperatorTestConfig(t, 1)
	nonCoordEntClient := db.NewPostgresEntClientForIntegrationTest(t, nonCoordOperatorConfig.DatabasePath)
	defer nonCoordEntClient.Close()

	// Set the issuer transfer to Alice as REVEALED on a non-coordinator
	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, nonCoordEntClient, transferToAliceHash)

	// Alice attempts a transfer to Bob
	aliceWalletConfig := wallet.NewTestWalletConfigWithIdentityKey(t, alicePrivKey)
	aliceTransferToBob, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, aliceWalletConfig, tokenTransactionParams{
		TokenIdentityPubKey:            tokenIdentityPubKey,
		TokenIdentifier:                tokenIdentifier,
		FinalIssueTokenTransactionHash: transferToAliceHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
		NumOutputsToSpend:              1,
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	// If Alice successfully transfers to Bob, just-in-time finalization of CREATED_SIGNED outputs was successful
	_, err = wallet.BroadcastTokenTransfer(
		ctx, aliceWalletConfig, aliceTransferToBob,
		[]keys.Private{alicePrivKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")
}

func createTransferTokenTransactionForWallet(
	t *testing.T,
	ctx context.Context,
) (*wallet.TestWalletConfig, []byte, *tokenpb.TokenTransaction, error) {
	now := utils.ToMicrosecondPrecision(time.Now().UTC())
	return createTransferTokenTransactionForWalletWithTimestamp(t, ctx, now)
}

func createTransferTokenTransactionForWalletWithTimestamp(t *testing.T, ctx context.Context, timestamp time.Time) (*wallet.TestWalletConfig, []byte, *tokenpb.TokenTransaction, error) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
	tokenIdentifier := queryTokenIdentifierOrFail(t, config, config.IdentityPrivateKey.Public())

	tokenPrivKey := config.IdentityPrivateKey
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

	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            tokenPrivKey.Public(),
		TokenIdentifier:                tokenIdentifier,
		FinalIssueTokenTransactionHash: finalIssueTokenTransactionHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
		ClientCreatedTimestamp:         timestamp,
	})
	require.NoError(t, err, "failed to create test token transfer transaction")

	transferTokenTransactionResponse, err := broadcastTokenTransaction(
		t,
		ctx,
		config,
		transferTokenTransaction,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	finalTransferTokenTransactionHash, err := utils.HashTokenTransaction(transferTokenTransactionResponse, false)
	require.NoError(t, err, "failed to hash transfer token transaction")
	return config, finalTransferTokenTransactionHash, transferTokenTransactionResponse, nil
}

func setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t *testing.T, ctx context.Context, entClient *ent.Client, finalTransferTokenTransactionHash []byte) {
	tx, err := entClient.Tx(ctx)
	require.NoError(t, err)

	tokenTransaction, err := tx.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithSpentOutput().
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	createdIDs := make([]uuid.UUID, len(tokenTransaction.Edges.CreatedOutput))
	for i, o := range tokenTransaction.Edges.CreatedOutput {
		createdIDs[i] = o.ID
	}

	spentIDs := make([]uuid.UUID, len(tokenTransaction.Edges.SpentOutput))
	for i, o := range tokenTransaction.Edges.SpentOutput {
		spentIDs[i] = o.ID
	}

	err = tx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(createdIDs...)).
		SetStatus(st.TokenOutputStatusCreatedSigned).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(spentIDs...)).
		SetStatus(st.TokenOutputStatusSpentSigned).
		Exec(ctx)
	require.NoError(t, err)

	_, err = tx.TokenPartialRevocationSecretShare.
		Delete().
		Where(tokenpartialrevocationsecretshare.HasTokenOutputWith(
			tokenoutput.IDIn(spentIDs...),
		)).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.TokenTransaction.Update().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		SetStatus(st.TokenTransactionStatusRevealed).
		SetUpdateTime(time.Now().Add(-25 * time.Minute).UTC()).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.Commit()
	require.NoError(t, err)

	tokenTransaction, err = entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)

	require.Equal(t, st.TokenTransactionStatusRevealed, tokenTransaction.Status, "token transaction status should be revealed")
	require.Greater(t, time.Now().In(time.UTC).Sub(tokenTransaction.UpdateTime.In(time.UTC)), 5*time.Minute, "update time should be more than 5 minutes before now")
	for _, output := range tokenTransaction.Edges.SpentOutput {
		require.Equal(t, st.TokenOutputStatusSpentSigned, output.Status, "spent output %s should be signed", output.ID)
		require.Empty(t, output.Edges.TokenPartialRevocationSecretShares, "should have 0 secret shares")
	}
	for _, output := range tokenTransaction.Edges.CreatedOutput {
		require.Equal(t, st.TokenOutputStatusCreatedSigned, output.Status, "created output %s should be signed", output.ID)
	}
}

func setAndValidateSuccessfulTokenTransactionToRevealedWithoutDeletingRevocationSecretShares(t *testing.T, ctx context.Context, entClient *ent.Client, finalTransferTokenTransactionHash []byte) {
	tx, err := entClient.Tx(ctx)
	require.NoError(t, err)

	tokenTransaction, err := tx.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	createdIDs := make([]uuid.UUID, 0, len(tokenTransaction.Edges.CreatedOutput))
	for _, o := range tokenTransaction.Edges.CreatedOutput {
		createdIDs = append(createdIDs, o.ID)
	}

	spentIDs := make([]uuid.UUID, 0, len(tokenTransaction.Edges.SpentOutput))
	for _, o := range tokenTransaction.Edges.SpentOutput {
		spentIDs = append(spentIDs, o.ID)
	}

	err = tx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(createdIDs...)).
		SetStatus(st.TokenOutputStatusCreatedSigned).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(spentIDs...)).
		SetStatus(st.TokenOutputStatusSpentSigned).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.TokenTransaction.Update().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		SetStatus(st.TokenTransactionStatusRevealed).
		SetUpdateTime(time.Now().Add(-25 * time.Minute).UTC()).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.Commit()
	require.NoError(t, err)

	updatedTokenTx, err := entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)

	require.Equal(t, st.TokenTransactionStatusRevealed, updatedTokenTx.Status, "token transaction status should be revealed")
	require.Greater(t, time.Now().In(time.UTC).Sub(updatedTokenTx.UpdateTime.In(time.UTC)), 5*time.Minute, "update time should be more than 5 minutes before now")
	for i, output := range updatedTokenTx.Edges.SpentOutput {
		require.Equal(t, st.TokenOutputStatusSpentSigned, output.Status, "spent output %s should be signed", output.ID)
		require.Len(t, output.Edges.TokenPartialRevocationSecretShares, len(tokenTransaction.Edges.SpentOutput[i].Edges.TokenPartialRevocationSecretShares), "should have the same amount of keyshares as the original transaction")
	}
	for _, output := range updatedTokenTx.Edges.CreatedOutput {
		require.Equal(t, st.TokenOutputStatusCreatedSigned, output.Status, "created output %s should be signed", output.ID)
	}
}

func setAndValidateSuccessfulTokenTransactionToStartedForOperator(t *testing.T, ctx context.Context, entClient *ent.Client, finalTransferTokenTransactionHash []byte) {
	tx, err := entClient.Tx(ctx)
	require.NoError(t, err)

	tokenTransaction, err := tx.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithSpentOutput().
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	createdIDs := make([]uuid.UUID, 0, len(tokenTransaction.Edges.CreatedOutput))
	for _, o := range tokenTransaction.Edges.CreatedOutput {
		createdIDs = append(createdIDs, o.ID)
	}

	spentIDs := make([]uuid.UUID, 0, len(tokenTransaction.Edges.SpentOutput))
	for _, o := range tokenTransaction.Edges.SpentOutput {
		spentIDs = append(spentIDs, o.ID)
	}

	err = tx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(createdIDs...)).
		SetStatus(st.TokenOutputStatusCreatedStarted).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(spentIDs...)).
		SetStatus(st.TokenOutputStatusSpentStarted).
		Exec(ctx)
	require.NoError(t, err)

	_, err = tx.TokenPartialRevocationSecretShare.
		Delete().
		Where(tokenpartialrevocationsecretshare.HasTokenOutputWith(
			tokenoutput.IDIn(spentIDs...),
		)).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.TokenTransaction.Update().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		SetStatus(st.TokenTransactionStatusStarted).
		ClearOperatorSignature().
		SetUpdateTime(time.Now().Add(-25 * time.Minute).UTC()).
		Exec(ctx)
	require.NoError(t, err)

	err = tx.Commit()
	require.NoError(t, err)

	tokenTransaction, err = entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)

	require.Equal(t, st.TokenTransactionStatusStarted, tokenTransaction.Status, "token transaction status should be started")
	require.Greater(t, time.Now().In(time.UTC).Sub(tokenTransaction.UpdateTime.In(time.UTC)), 5*time.Minute, "update time should be more than 5 minutes before now")
	for _, output := range tokenTransaction.Edges.SpentOutput {
		require.Equal(t, st.TokenOutputStatusSpentStarted, output.Status, "spent output %s should be started", output.ID)
		require.Empty(t, output.Edges.TokenPartialRevocationSecretShares, "should have 0 secret shares")
	}
	for _, output := range tokenTransaction.Edges.CreatedOutput {
		require.Equal(t, st.TokenOutputStatusCreatedStarted, output.Status, "created output %s should be started", output.ID)
	}
}

func TestQueryTokenOutputsWithRevealedRevocationSecrets(t *testing.T) {
	if broadcastTokenTestsUseV3 {
		t.Skip("Skipping test for V3 transactions as V3 flow would finalize the transaction during broadcast.")
	}

	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	issuerPrivKey := config.IdentityPrivateKey
	tokenIdentifier := queryTokenIdentifierOrFail(t, config, issuerPrivKey.Public())

	mintTx, owner1PrivKey, owner2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public(), tokenIdentifier)
	require.NoError(t, err, "failed to create mint transaction")

	finalTokenTransaction, err := broadcastTokenTransaction(
		t,
		t.Context(),
		config,
		mintTx,
		[]keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast mint transaction")

	mintTxHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	require.NoError(t, err, "failed to hash mint transaction")

	transferTx, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            issuerPrivKey.Public(),
		TokenIdentifier:                tokenIdentifier,
		FinalIssueTokenTransactionHash: mintTxHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
	})
	require.NoError(t, err, "failed to create transfer transaction")

	startResp, finalTxHash, err := startTokenTransactionOrBroadcast(t, t.Context(), config, transferTx, []keys.Private{owner1PrivKey, owner2PrivKey}, 1*time.Second)
	require.NoError(t, err, "failed to start transfer transaction")
	require.NotNil(t, startResp)

	operatorSignatures, err := wallet.CreateOperatorSpecificSignatures(
		config,
		[]keys.Private{owner1PrivKey, owner2PrivKey},
		finalTxHash,
	)
	require.NoError(t, err, "failed to create operator-specific signatures")

	allOperatorSignatures := make(map[string][]byte)
	for _, operator := range config.SigningOperators {
		var foundOperatorSignatures *tokenpb.InputTtxoSignaturesPerOperator
		for _, sig := range operatorSignatures {
			sigOperatorIDPubKey, err := keys.ParsePublicKey(sig.OperatorIdentityPublicKey)
			require.NoError(t, err)
			if sigOperatorIDPubKey.Equals(operator.IdentityPublicKey) {
				foundOperatorSignatures = sig
				break
			}
		}
		require.NotNil(t, foundOperatorSignatures, "expected to find signatures for operator %s", operator.Identifier)

		signResp, err := wallet.SignTokenTransactionFromCoordination(
			t.Context(),
			config,
			wallet.SignTokenTransactionFromCoordinationParams{
				Operator:         operator,
				TokenTransaction: startResp.FinalTokenTransaction,
				FinalTxHash:      finalTxHash,
				OwnerPrivateKeys: []keys.Private{owner1PrivKey, owner2PrivKey},
			},
		)
		require.NoError(t, err, "failed to sign with operator %s", operator.Identifier)
		require.NotNil(t, signResp)

		allOperatorSignatures[operator.Identifier] = signResp.SparkOperatorSignature
	}

	require.NoError(t, err, "failed to query token outputs before transaction")

	require.Len(t, allOperatorSignatures, len(config.SigningOperators), "expected signatures from all operators")

	revocationShares, err := wallet.PrepareRevocationSharesFromCoordinator(
		t.Context(),
		config,
		startResp.FinalTokenTransaction,
	)
	require.NoError(t, err, "failed to prepare revocation shares for testing")

	exchangingOperator := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000002"]
	require.NotNil(t, exchangingOperator, "expected a non-coordinator operator")

	err = wallet.ExchangeRevocationSecretsManually(
		t.Context(),
		config,
		wallet.ExchangeRevocationSecretsParams{
			FinalTokenTransaction: startResp.FinalTokenTransaction,
			FinalTxHash:           finalTxHash,
			AllOperatorSignatures: allOperatorSignatures,
			RevocationShares:      revocationShares,
			TargetOperator:        exchangingOperator,
		},
	)
	require.NoError(t, err, "failed to exchange revocation secrets manually with operator %s", exchangingOperator.Identifier)
	time.Sleep(time.Second)

	queryAndVerifyNoTokenOutputs(t, []string{exchangingOperator.Identifier}, owner1PrivKey)

	var unexchangedOperatorIdentifiers []string
	for identifier := range config.SigningOperators {
		if identifier != exchangingOperator.Identifier {
			unexchangedOperatorIdentifiers = append(unexchangedOperatorIdentifiers, identifier)
		}
	}
	queryAndVerifyTokenOutputs(t, unexchangedOperatorIdentifiers, finalTokenTransaction, owner1PrivKey)
}

// This function takes a successful tokentransaction and remaps the spent outputs on the non-coordinator
// from the "initial" transaction to the "remap" transaction.
// It also sets the statuses on both to the provided status.
// The goal is to test transactions in different states across the coordinator and non-coordinator
// can be properly finalized by the finalize_revealed_token_transactions cron job.
func setAndValidateSuccessfulTokenTransactionToStatusAndRemapSpentOutputsForOperator(
	t *testing.T,
	ctx context.Context,
	senderWalletConfig *wallet.TestWalletConfig,
	nonCoordinatorOperatorConfig *so.Config,
	coordinatorEntClient *ent.Client,
	nonCoordEntClient *ent.Client,
	initialFinalTokenTransactionHash []byte,
	nonCoordinatorInitialTxStatus st.TokenTransactionStatus,
	nonCoordinatorRemapTxStatus st.TokenTransactionStatus,
	expiredRemapTx bool,
	remapClientCreatedTimestamp time.Time,
) {
	coordinatorTx, err := coordinatorEntClient.Tx(ctx)
	require.NoError(t, err)

	tokenTransaction, err := coordinatorTx.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(initialFinalTokenTransactionHash)).
		WithSpentOutput(func(q *ent.TokenOutputQuery) {
			q.WithOutputCreatedTokenTransaction()
		}).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	initialTxCoordinatorCreatedOutputIDs := make([]uuid.UUID, 0, len(tokenTransaction.Edges.CreatedOutput))
	for _, o := range tokenTransaction.Edges.CreatedOutput {
		initialTxCoordinatorCreatedOutputIDs = append(initialTxCoordinatorCreatedOutputIDs, o.ID)
	}

	initialTxCoordinatorSpentOutputIDs := make([]uuid.UUID, 0, len(tokenTransaction.Edges.SpentOutput))
	for _, o := range tokenTransaction.Edges.SpentOutput {
		initialTxCoordinatorSpentOutputIDs = append(initialTxCoordinatorSpentOutputIDs, o.ID)
	}

	// Set the coordinator's initial transaction to REVEALED.
	err = coordinatorTx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(initialTxCoordinatorCreatedOutputIDs...)).
		SetStatus(st.TokenOutputStatusCreatedSigned).
		Exec(ctx)
	require.NoError(t, err)

	err = coordinatorTx.TokenOutput.
		Update().
		Where(tokenoutput.IDIn(initialTxCoordinatorSpentOutputIDs...)).
		SetStatus(st.TokenOutputStatusSpentSigned).
		Exec(ctx)
	require.NoError(t, err)

	_, err = coordinatorTx.TokenPartialRevocationSecretShare.
		Delete().
		Where(tokenpartialrevocationsecretshare.HasTokenOutputWith(
			tokenoutput.IDIn(initialTxCoordinatorSpentOutputIDs...),
		)).
		Exec(ctx)
	require.NoError(t, err)

	err = coordinatorTx.TokenTransaction.Update().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(initialFinalTokenTransactionHash)).
		SetStatus(st.TokenTransactionStatusRevealed).
		SetUpdateTime(time.Now().Add(-25 * time.Minute).UTC()).
		Exec(ctx)
	require.NoError(t, err)

	err = coordinatorTx.Commit()
	require.NoError(t, err)

	tokenTransaction, err = coordinatorEntClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(initialFinalTokenTransactionHash)).
		WithPeerSignatures().
		WithSpentOutput(
			func(to *ent.TokenOutputQuery) {
				to.WithTokenPartialRevocationSecretShares()
				to.WithOutputCreatedTokenTransaction()
			},
		).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)

	require.Equal(t, st.TokenTransactionStatusRevealed, tokenTransaction.Status, "token transaction status should be revealed")
	require.Greater(t, time.Now().In(time.UTC).Sub(tokenTransaction.UpdateTime.In(time.UTC)), 5*time.Minute, "update time should be more than 5 minutes before now")
	for _, output := range tokenTransaction.Edges.CreatedOutput {
		require.Equal(t, st.TokenOutputStatusCreatedSigned, output.Status, "created output %s should be started", output.ID)
	}

	// ==== non-coordinator set up ====
	// =================================
	// The initial transaction will have its spent outputs remapped to the remap transaction.
	// Set up both transactions to the provided status.
	err = nonCoordEntClient.TokenTransaction.Update().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(initialFinalTokenTransactionHash)).
		SetStatus(nonCoordinatorInitialTxStatus).
		Exec(ctx)
	require.NoError(t, err)

	var initialTxCreatedOutputStatus st.TokenOutputStatus
	switch nonCoordinatorInitialTxStatus {
	case st.TokenTransactionStatusStarted:
		initialTxCreatedOutputStatus = st.TokenOutputStatusCreatedStarted
	case st.TokenTransactionStatusSigned:
		initialTxCreatedOutputStatus = st.TokenOutputStatusCreatedSigned
	default:
		t.Fatalf("Unsupported token transaction status: %s", nonCoordinatorInitialTxStatus)
	}

	nonCoordInitialTx, err := nonCoordEntClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(initialFinalTokenTransactionHash)).
		WithCreatedOutput().
		WithSpentOutput(func(q *ent.TokenOutputQuery) {
			q.WithOutputCreatedTokenTransaction()
		}).
		Only(ctx)
	require.NoError(t, err)

	nonCoordCreatedOutputIDs := make([]uuid.UUID, 0, len(nonCoordInitialTx.Edges.CreatedOutput))
	for _, o := range nonCoordInitialTx.Edges.CreatedOutput {
		nonCoordCreatedOutputIDs = append(nonCoordCreatedOutputIDs, o.ID)
	}

	err = nonCoordEntClient.TokenOutput.Update().
		Where(tokenoutput.IDIn(nonCoordCreatedOutputIDs...)).
		SetStatus(initialTxCreatedOutputStatus).
		Exec(ctx)
	require.NoError(t, err)

	_, err = createRemapTransactionSpendingSameOutputsOnNonCoordinator(
		t, ctx,
		senderWalletConfig,
		nonCoordinatorOperatorConfig,
		nonCoordEntClient,
		tokenTransaction,
		nonCoordinatorRemapTxStatus,
		remapClientCreatedTimestamp,
		expiredRemapTx,
	)
	require.NoError(t, err, "failed to create remap transaction")

	initialNonCoordTxAfterRemap, err := nonCoordEntClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(initialFinalTokenTransactionHash)).
		WithSpentOutput().
		Only(ctx)
	require.NoError(t, err, "failed to query initial transaction")
	require.Equal(t, nonCoordinatorInitialTxStatus, initialNonCoordTxAfterRemap.Status)
	require.Empty(t, initialNonCoordTxAfterRemap.Edges.SpentOutput, "should have no more spent outputs after remap")
}

// createRemapTransactionSpendingSameOutputsOnNonCoordinator creates a competing "remap" transaction that
// spends the same outputs as the initial transaction, and writes it directly to the non-coordinator's database.
// This simulates a race condition where the non-coordinator receives a competing transaction.
func createRemapTransactionSpendingSameOutputsOnNonCoordinator(
	t *testing.T,
	ctx context.Context,
	senderWalletConfig *wallet.TestWalletConfig,
	nonCoordinatorConfig *so.Config,
	nonCoordEntClient *ent.Client,
	initialTx *ent.TokenTransaction,
	nonCoordinatorRemapTxStatus st.TokenTransactionStatus,
	remapTimestamp time.Time,
	expiredRemapTx bool,
) ([]byte, error) {
	t.Helper()

	// Extract spent outputs from the initial transaction
	spentOutputs := initialTx.Edges.SpentOutput
	require.NotEmpty(t, spentOutputs, "initial tx must have spent outputs")

	// Build the outputsToSpend for the remap transaction (same as initial TX)
	// Collect (hash, vout) pairs to identify outputs across databases
	outputsToSpend := make([]*tokenpb.TokenOutputToSpend, len(spentOutputs))
	var tokenIdentifier []byte
	var spentOutputPredicates []predicate.TokenOutput

	for i, output := range spentOutputs {
		createdByTx := output.Edges.OutputCreatedTokenTransaction
		require.NotNil(t, createdByTx, "spent output must have a creating transaction")

		outputsToSpend[i] = &tokenpb.TokenOutputToSpend{
			PrevTokenTransactionHash: createdByTx.FinalizedTokenTransactionHash,
			PrevTokenTransactionVout: uint32(output.CreatedTransactionOutputVout),
		}

		// Build predicate to match this output by (hash, vout) in non-coordinator DB
		spentOutputPredicates = append(spentOutputPredicates, tokenoutput.And(
			tokenoutput.CreatedTransactionFinalizedHashEQ(output.CreatedTransactionFinalizedHash),
			tokenoutput.CreatedTransactionOutputVoutEQ(output.CreatedTransactionOutputVout),
		))

		if i == 0 {
			tokenIdentifier = output.TokenIdentifier
		}
	}

	now := time.Now().UTC()
	expiryTime := now.Add(5 * time.Minute)
	if expiredRemapTx {
		expiryTime = now.Add(-25 * time.Minute)
	}

	remapTransferPb, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, senderWalletConfig, tokenTransactionParams{
		TokenIdentityPubKey:            senderWalletConfig.IdentityPrivateKey.Public(),
		TokenIdentifier:                tokenIdentifier,
		FinalIssueTokenTransactionHash: outputsToSpend[0].PrevTokenTransactionHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{testTransferOutput1Amount},
		ClientCreatedTimestamp:         remapTimestamp,
	})
	require.NoError(t, err, "failed to create remap transaction proto")

	remapTransferPb.ExpiryTime = timestamppb.New(expiryTime)

	remapTransferPb.TokenInputs = &tokenpb.TokenTransaction_TransferInput{
		TransferInput: &tokenpb.TokenTransferInput{
			OutputsToSpend: outputsToSpend,
		},
	}

	for _, output := range remapTransferPb.TokenOutputs {
		outputId := uuid.New().String()
		output.Id = &outputId

		revocationPrivKey := keys.GeneratePrivateKey()
		output.RevocationCommitment = revocationPrivKey.Public().Serialize()
	}

	partialHash, err := utils.HashTokenTransaction(remapTransferPb, true)
	require.NoError(t, err, "failed to compute partial hash")

	finalHash, err := utils.HashTokenTransaction(remapTransferPb, false)
	require.NoError(t, err, "failed to compute final hash")

	var createdOutputStatus st.TokenOutputStatus
	var spentOutputStatus st.TokenOutputStatus
	switch nonCoordinatorRemapTxStatus {
	case st.TokenTransactionStatusStarted:
		createdOutputStatus = st.TokenOutputStatusCreatedStarted
		spentOutputStatus = st.TokenOutputStatusSpentStarted
	case st.TokenTransactionStatusSigned:
		createdOutputStatus = st.TokenOutputStatusCreatedSigned
		spentOutputStatus = st.TokenOutputStatusSpentSigned
	default:
		return nil, fmt.Errorf("unsupported remap transaction status: %s", nonCoordinatorRemapTxStatus)
	}

	remapTxEnt, err := nonCoordEntClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(nonCoordinatorRemapTxStatus).
		SetCreateTime(now).
		SetUpdateTime(now.Add(-25 * time.Minute)).
		SetExpiryTime(expiryTime).
		SetClientCreatedTimestamp(remapTimestamp).
		SetVersion(st.TokenTransactionVersion(remapTransferPb.Version)).
		SetCoordinatorPublicKey(nonCoordinatorConfig.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err, "failed to create remap transaction entity")

	if remapTransferPb.ValidityDurationSeconds != nil {
		_, err = nonCoordEntClient.TokenTransaction.UpdateOne(remapTxEnt).
			SetValidityDurationSeconds(*remapTransferPb.ValidityDurationSeconds).
			Save(ctx)
		require.NoError(t, err, "failed to set validity duration")
	}

	tokenCreate, err := nonCoordEntClient.TokenCreate.Query().
		Where(tokencreate.TokenIdentifierEQ(tokenIdentifier)).
		Only(ctx)
	require.NoError(t, err, "failed to get token create from non-coordinator")

	for i, protoOutput := range remapTransferPb.TokenOutputs {
		ownerPubKey, err := keys.ParsePublicKey(protoOutput.OwnerPublicKey)
		require.NoError(t, err, "failed to parse owner public key")

		secretShare := keys.GeneratePrivateKey()
		keyshare, err := nonCoordEntClient.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusInUse).
			SetSecretShare(secretShare).
			SetPublicKey(secretShare.Public()).
			SetPublicShares(map[string]keys.Public{
				"operator0": keys.GeneratePrivateKey().Public(),
				"operator1": keys.GeneratePrivateKey().Public(),
				"operator2": keys.GeneratePrivateKey().Public(),
			}).
			SetMinSigners(2).
			SetCoordinatorIndex(1).
			Save(ctx)
		require.NoError(t, err, "failed to create keyshare")

		_, err = nonCoordEntClient.TokenOutput.Create().
			SetTokenIdentifier(protoOutput.TokenIdentifier).
			SetOwnerPublicKey(ownerPubKey).
			SetTokenAmount(protoOutput.TokenAmount).
			SetCreatedTransactionFinalizedHash(finalHash).
			SetCreatedTransactionOutputVout(int32(i)).
			SetStatus(createdOutputStatus).
			SetOutputCreatedTokenTransaction(remapTxEnt).
			SetWithdrawBondSats(1000000).
			SetTokenCreate(tokenCreate).
			SetWithdrawRelativeBlockLocktime(1000).
			SetWithdrawRevocationCommitment(protoOutput.RevocationCommitment).
			SetRevocationKeyshare(keyshare).
			Save(ctx)
		require.NoError(t, err, "failed to create created token outputs for remap transaction")
	}

	_, err = nonCoordEntClient.TokenPartialRevocationSecretShare.
		Delete().
		Where(tokenpartialrevocationsecretshare.HasTokenOutputWith(
			tokenoutput.Or(spentOutputPredicates...),
		)).
		Exec(ctx)
	require.NoError(t, err, "failed to delete old partial revocation shares for remapped outputs")

	err = nonCoordEntClient.TokenOutput.Update().
		Where(tokenoutput.Or(spentOutputPredicates...)).
		SetStatus(spentOutputStatus).
		SetOutputSpentTokenTransaction(remapTxEnt).
		ClearSpentRevocationSecret().
		Exec(ctx)
	require.NoError(t, err, "failed to remap the spent outputs and update status on remap transaction")

	createdRemapTx, err := nonCoordEntClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalHash)).
		WithSpentOutput(func(q *ent.TokenOutputQuery) {
			q.WithOutputSpentTokenTransaction()
		}).
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err, "failed to query created remap transaction")
	require.Equal(t, nonCoordinatorRemapTxStatus, createdRemapTx.Status)
	require.Len(t, createdRemapTx.Edges.CreatedOutput, 1, "should have 1 created output")
	require.Len(t, createdRemapTx.Edges.SpentOutput, len(spentOutputs), "should have same number of spent outputs as initial tx")

	for _, output := range createdRemapTx.Edges.CreatedOutput {
		require.Equal(t, createdOutputStatus, output.Status)
		require.Equal(t, finalHash, output.CreatedTransactionFinalizedHash)
	}
	for _, output := range createdRemapTx.Edges.SpentOutput {
		require.Equal(t, spentOutputStatus, output.Status)
		require.Equal(t, remapTxEnt.ID, output.Edges.OutputSpentTokenTransaction.ID)
	}

	return finalHash, nil
}
