package tokens_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokenpartialrevocationsecretshare"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/tokentransactionpeersignature"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

func TestRevocationExchangeCronJobSuccessfullyFinalizesRevealed(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err, "failed to create transfer token transaction")

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, entClient, finalTransferTokenTransactionHash)

	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
	require.NoError(t, err)
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

func TestRevocationExchangeCronJobSuccessfullyFinalizesRevealedWithAllFieldsButStatusRevealed(t *testing.T) {
	ctx := t.Context()
	config, finalTransferTokenTransactionHash, err := createTransferTokenTransactionForWallet(t, ctx)
	require.NoError(t, err, "failed to create transfer token transaction")

	entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer entClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedWithoutDeletingRevocationSecretShares(t, ctx, entClient, finalTransferTokenTransactionHash)

	conn, err := config.SigningOperators["0000000000000000000000000000000000000000000000000000000000000001"].NewOperatorGRPCConnection()
	require.NoError(t, err)
	mockClient := pbmock.NewMockServiceClient(conn)
	_, err = mockClient.TriggerTask(t.Context(), &pbmock.TriggerTaskRequest{TaskName: "finalize_revealed_token_transactions"})
	require.NoError(t, err)
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
	config, finalTransferTokenTransactionHash, err := createTransferTokenTransactionForWallet(t, ctx)
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
	require.NoError(t, err)
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
	config, finalTransferTokenTransactionHash, err := createTransferTokenTransactionForWallet(t, ctx)
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
	config, finalTransferTokenTransactionHash, err := createTransferTokenTransactionForWallet(t, ctx)
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
	require.NoError(t, err)
	conn.Close()

	tokenTransactionAfterFinalizeRevealedTransactions, err := entClient.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
		WithPeerSignatures().
		WithCreatedOutput().
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TokenTransactionStatusRevealed, tokenTransactionAfterFinalizeRevealedTransactions.Status)
}

func createTransferTokenTransactionForWallet(t *testing.T, ctx context.Context) (*wallet.TestWalletConfig, []byte, error) {
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(
		t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, _, err := createTestTokenTransferTransactionTokenPb(t,
		config,
		finalIssueTokenTransactionHash,
		tokenPrivKey.Public(),
	)
	require.NoError(t, err, "failed to create test token transfer transaction")

	transferTokenTransactionResponse, err := wallet.BroadcastTokenTransfer(
		ctx, config, transferTokenTransaction,
		[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	finalTransferTokenTransactionHash, err := utils.HashTokenTransaction(transferTokenTransactionResponse, false)
	require.NoError(t, err, "failed to hash transfer token transaction")
	return config, finalTransferTokenTransactionHash, nil
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
		t.Logf("spent output %s", o.ID)
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
		t.Logf("spent output %s", o.ID)
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
		t.Logf("spent output %s", o.ID)
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
	config := wallet.NewTestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	issuerPrivKey := config.IdentityPrivateKey
	mintTx, owner1PrivKey, owner2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create mint transaction")

	finalTokenTransaction, err := wallet.BroadcastTokenTransfer(
		t.Context(), config, mintTx, []keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast mint transaction")

	mintTxHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	require.NoError(t, err, "failed to hash mint transaction")

	transferTx, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            issuerPrivKey.Public(),
		FinalIssueTokenTransactionHash: mintTxHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
	})
	require.NoError(t, err, "failed to create transfer transaction")

	startResp, finalTxHash, err := wallet.StartTokenTransaction(t.Context(), config, transferTx, []keys.Private{owner1PrivKey, owner2PrivKey}, 1*time.Second, nil)
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
