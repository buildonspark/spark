package grpctest

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokenpartialrevocationsecretshare"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/tokentransactionpeersignature"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	TestValidityDurationSecs      = 30
	TestValidityDurationSecsPlus1 = TestValidityDurationSecs + 1
	TooLongValidityDurationSecs   = 300 + 1
	TooShortValidityDurationSecs  = 0
	TokenTransactionVersion1      = 1
	TokenTransactionVersion2      = 2
	TokenTransactionVersion3      = 3
)

// Parameter structs for WithParams functions
type tokenTransactionParams struct {
	TokenIdentityPubKey            keys.Public
	IsNativeSparkToken             bool
	UseTokenIdentifier             bool
	FinalIssueTokenTransactionHash []byte   // Only used for transfers, nil for mints
	NumOutputs                     int      // Number of outputs to create (defaults to 2 for backward compatibility)
	OutputAmounts                  []uint64 // Exact amounts for each output (must match NumOutputs length)
	MintToSelf                     bool
	InvoiceAttachments             []*tokenpb.InvoiceAttachment
	Version                        int // Optional explicit token transaction version (defaults to V2 if 0)
}

type createNativeSparkTokenParams struct {
	IssuerPrivateKey keys.Private
	Name             string
	Ticker           string
	MaxSupply        uint64
}

type sparkTokenCreationTestParams struct {
	issuerPrivateKey keys.Private
	name             string
	ticker           string
	maxSupply        uint64
	expectedError    bool // optional, defaults to false
}

// QueryTokenTransactionsParams is no longer used - individual parameters are now passed directly to QueryTokenTransactionsV2

var signatureTypeTestCases = []struct {
	name                 string
	useSchnorrSignatures bool
}{
	{
		name:                 "ECDSA signatures",
		useSchnorrSignatures: false,
	},
	{
		name:                 "Schnorr signatures",
		useSchnorrSignatures: true,
	},
}

func TestCoordinatedL1TokenMint(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			tokenPrivKey := config.IdentityPrivateKey

			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")
			require.Len(t, finalIssueTokenTransaction.TokenOutputs, 2, "expected 2 created outputs in mint transaction")

			userOneConfig := sparktesting.TestWalletConfigWithIdentityKey(t, userOutput1PrivKey)
			userTwoConfig := sparktesting.TestWalletConfigWithIdentityKey(t, userOutput2PrivKey)

			userOneBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userOneConfig,
				[]keys.Public{userOneConfig.IdentityPublicKey()},
				[]keys.Public{tokenPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user one token outputs")

			userTwoBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userTwoConfig,
				[]keys.Public{userTwoConfig.IdentityPublicKey()},
				[]keys.Public{tokenPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user two token outputs")

			require.Len(t, userOneBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user one")
			userOneAmount := bytesToBigInt(userOneBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(TestIssueOutput1Amount), userOneAmount, "user one should have the first mint output amount")

			require.Len(t, userTwoBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user two")
			userTwoAmount := bytesToBigInt(userTwoBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(TestIssueOutput2Amount), userTwoAmount, "user two should have the second mint output amount")
		})
	}
}

// TestCoordinatedL1TokenMintAndTransfer tests the full coordinated flow with mint and transfer for an L1 token.
func TestCoordinatedL1TokenMintAndTransfer(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey})
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			for i, output := range finalIssueTokenTransaction.TokenOutputs {
				if output.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
					t.Errorf("output %d: expected withdrawal bond sats 10000, got %d", i, output.GetWithdrawBondSats())
				}
				if output.GetWithdrawRelativeBlockLocktime() != uint64(WithdrawalRelativeBlockLocktimeInConfig) {
					t.Errorf("output %d: expected withdrawal relative block locktime 1000, got %d", i, output.GetWithdrawRelativeBlockLocktime())
				}
			}

			finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
			require.NoError(t, err, "failed to hash final issuance token transaction")

			transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransactionTokenPb(t, config,
				finalIssueTokenTransactionHash,
				tokenPrivKey.Public(),
			)
			require.NoError(t, err, "failed to create test token transfer transaction")
			userOutput3PubKeyBytes := userOutput3PrivKey.Public().Serialize()

			transferTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, transferTokenTransaction, []keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast transfer token transaction")

			finalTransferTokenTransactionHash, err := utils.HashTokenTransaction(transferTokenTransactionResponse, false)
			require.NoError(t, err, "failed to hash transfer token transaction")

			entClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
			defer entClient.Close()

			numOperators := len(config.SigningOperators)
			tokenTransaction, err := entClient.TokenTransaction.Query().
				Where(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)).
				WithPeerSignatures().
				WithSpentOutput(
					func(to *ent.TokenOutputQuery) {
						to.WithTokenPartialRevocationSecretShares()
					},
				).
				WithCreatedOutput().
				Only(t.Context())
			require.NoError(t, err)
			// We expect to see one peer signature per peer operator.
			require.Len(t, tokenTransaction.Edges.PeerSignatures, numOperators-1, "should have exactly numOperators-1 peer signatures")
			require.Equal(t, st.TokenTransactionStatusFinalized, tokenTransaction.Status)

			spentTokenOutputs := tokenTransaction.Edges.SpentOutput
			require.Len(t, spentTokenOutputs, 2, "should have exactly two spent outputs")

			createdTokenOutputs := tokenTransaction.Edges.CreatedOutput
			require.Len(t, createdTokenOutputs, 1, "should have exactly one created output")
			require.Equal(t, st.TokenOutputStatusCreatedFinalized, createdTokenOutputs[0].Status)
			createdTokenAmount := new(big.Int).SetBytes(createdTokenOutputs[0].TokenAmount)
			expectedCreatedTokenAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferOutput1Amount))
			require.Equal(t, expectedCreatedTokenAmount, createdTokenAmount, "created token amount does not match expected")

			// We expect to see both the tokenPublicKey and the tokenIdentifier
			require.Equal(t, tokenPrivKey.Public(), createdTokenOutputs[0].TokenPublicKey)
			require.NotNil(t, createdTokenOutputs[0].TokenIdentifier, "tokenIdentifier should not be nil")

			for _, tokenOutput := range spentTokenOutputs {
				shares := tokenOutput.Edges.TokenPartialRevocationSecretShares
				require.Len(t, shares, numOperators-1,
					"tokenOutput %s should have %d secret-share rows", tokenOutput.ID, numOperators-1)

				seenSecrets := make(map[string]struct{})
				seenOperators := make(map[keys.Public]struct{})

				for _, s := range shares {
					seenSecrets[string(s.SecretShare)] = struct{}{}
					seenOperators[s.OperatorIdentityPublicKey] = struct{}{}
				}

				// We expect to see one secret share per operator, except for the coordinator
				// We expect to see a 32 byte revocation secret for each output
				require.Len(t, seenSecrets, numOperators-1,
					"tokenOutput %s has duplicate secret-share blobs", tokenOutput.ID)
				require.Len(t, seenOperators, numOperators-1,
					"tokenOutput %s has duplicate operator-identity keys", tokenOutput.ID)
				require.NotNil(t, tokenOutput.SpentRevocationSecret, "tokenOutput %s has no revocation secret", tokenOutput.ID)
				require.Len(t, tokenOutput.SpentRevocationSecret, 32, "tokenOutput %s revocation secret does not match commitment size", tokenOutput.ID)
			}

			require.Len(t, transferTokenTransactionResponse.TokenOutputs, 1, "expected 1 created output in transfer transaction")
			transferAmount := new(big.Int).SetBytes(transferTokenTransactionResponse.TokenOutputs[0].TokenAmount)
			expectedTransferAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferOutput1Amount))
			require.Equal(t, 0, transferAmount.Cmp(expectedTransferAmount), "transfer amount does not match expected")
			require.Equal(t, userOutput3PubKeyBytes, transferTokenTransactionResponse.TokenOutputs[0].OwnerPublicKey, "transfer created output owner public key does not match expected")
		})
	}
}

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

	// ==== Verify the transaction is finalized ====
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

	// ==== Verify the transaction is finalized ====
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

	nonCoordOperatorConfig, err := sparktesting.SpecificOperatorTestConfig(1)
	require.NoError(t, err, "failed to get non-coordinator operator config")
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

	// ==== Verify the transaction is finalized ====
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

	nonCoordOperatorConfig, err := sparktesting.SpecificOperatorTestConfig(1)
	require.NoError(t, err, "failed to get non-coordinator operator config")
	nonCoordEntClient = db.NewPostgresEntClientForIntegrationTest(t, nonCoordOperatorConfig.DatabasePath)
	defer nonCoordEntClient.Close()

	setAndValidateSuccessfulTokenTransactionToRevealedForOperator(t, ctx, nonCoordEntClient, finalTransferTokenTransactionHash)
	peerSignature, err := nonCoordEntClient.TokenTransactionPeerSignature.Query().
		Where(tokentransactionpeersignature.And(
			tokentransactionpeersignature.HasTokenTransactionWith(tokentransaction.FinalizedTokenTransactionHashEQ(finalTransferTokenTransactionHash)),
			tokentransactionpeersignature.OperatorIdentityPublicKeyEQ(config.SigningOperators[config.CoordinatorIdentifier].IdentityPublicKey.Serialize()),
		)).Only(ctx)
	require.NoError(t, err)
	// Reset the peer signature to its original value after the test
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

	// ==== Verify the transaction is still STARTED ====
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

func createTransferTokenTransactionForWallet(t *testing.T, ctx context.Context) (*wallet.TestWalletConfig, []byte, error) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	// ==== Make a valid token transaction ====
	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
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

	transferTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
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
		fmt.Println("spent output", o.ID)
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
		fmt.Println("spent output", o.ID)
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
		fmt.Println("spent output", o.ID)
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

// TestCoordinatedTokenMintAndTransferTokensWithExpectedOutputAndTxRetrieval tests the full coordinated flow with mint and transfer
// This test also verifies that upon success that the expected outputs and transactions are retrievable.
func TestCoordinatedTokenMintAndTransferExpectedOutputAndTxRetrieval(t *testing.T) {
	// Use a fresh issuer key for this test to avoid cross-test interference.
	issuerPrivKey := getRandomPrivateKey(t)
	config := sparktesting.TestWalletConfigWithIdentityKey(t, issuerPrivKey)

	// Create a native Spark token for this issuer so that subsequent
	// mint/transfer operations are scoped to this isolated token.
	err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
		IssuerPrivateKey: issuerPrivKey,
		Name:             TestTokenName,
		Ticker:           TestTokenTicker,
		MaxSupply:        TestTokenMaxSupply,
	})
	require.NoError(t, err, "failed to create native spark token")

	tokenPrivKey := config.IdentityPrivateKey
	issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	for i, output := range finalIssueTokenTransaction.TokenOutputs {
		if output.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
			t.Errorf("output %d: expected withdrawal bond sats 10000, got %d", i, output.GetWithdrawBondSats())
		}
		if output.GetWithdrawRelativeBlockLocktime() != uint64(WithdrawalRelativeBlockLocktimeInConfig) {
			t.Errorf("output %d: expected withdrawal relative block locktime 1000, got %d", i, output.GetWithdrawRelativeBlockLocktime())
		}
	}

	finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issuance token transaction")

	transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransactionTokenPb(t,
		config,
		finalIssueTokenTransactionHash,
		tokenPrivKey.Public(),
	)
	require.NoError(t, err, "failed to create test token transfer transaction")
	userOutput3PubKeyBytes := userOutput3PrivKey.Public().Serialize()

	transferTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, transferTokenTransaction, []keys.Private{userOutput1PrivKey, userOutput2PrivKey},
	)
	require.NoError(t, err, "failed to broadcast transfer token transaction")

	require.Len(t, transferTokenTransactionResponse.TokenOutputs, 1, "expected 1 created output in transfer transaction")
	transferAmount := new(big.Int).SetBytes(transferTokenTransactionResponse.TokenOutputs[0].TokenAmount)
	expectedTransferAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestTransferOutput1Amount))
	require.Equal(t, 0, transferAmount.Cmp(expectedTransferAmount), "transfer amount does not match expected")
	require.Equal(t, userOutput3PubKeyBytes, transferTokenTransactionResponse.TokenOutputs[0].OwnerPublicKey, "transfer created output owner public key does not match expected")

	tokenOutputsResponse, err := wallet.QueryTokenOutputsV2(
		t.Context(),
		config,
		[]keys.Public{userOutput3PrivKey.Public()},
		[]keys.Public{tokenPrivKey.Public()},
	)
	require.NoError(t, err, "failed to get owned token outputs")
	require.Len(t, tokenOutputsResponse.OutputsWithPreviousTransactionData, 1, "expected 1 output after transfer transaction")
	require.Equal(t, expectedTransferAmount, new(big.Int).SetBytes(tokenOutputsResponse.OutputsWithPreviousTransactionData[0].Output.TokenAmount), "expected correct amount after transfer transaction")

	// Test QueryTokenTransactionsNative with pagination - first page
	page1Params := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  []keys.Public{tokenPrivKey.Public()},
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: nil,
		Offset:            0,
		Limit:             1,
	}
	tokenTransactionsPage1, err := wallet.QueryTokenTransactionsV2(
		t.Context(),
		config,
		page1Params,
	)
	require.NoError(t, err, "failed to query token transactions page 1")

	// Verify we got exactly 1 transaction
	require.Len(t, tokenTransactionsPage1.TokenTransactionsWithStatus, 1, "expected 1 token transaction in page 1")

	// Verify the offset is 1 (indicating there are more results)
	require.Equal(t, int64(1), tokenTransactionsPage1.Offset, "expected next offset 1 for page 1")

	// First transaction should be the transfer (reverse chronological)
	transferTx := tokenTransactionsPage1.TokenTransactionsWithStatus[0].TokenTransaction
	require.NotNil(t, transferTx.GetTransferInput(), "first transaction should be a transfer transaction")

	// Test QueryTokenTransactionsNative with pagination - second page
	page2Params := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  []keys.Public{tokenPrivKey.Public()},
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: nil,
		Offset:            tokenTransactionsPage1.Offset,
		Limit:             1,
	}
	tokenTransactionsPage2, err := wallet.QueryTokenTransactionsV2(t.Context(), config, page2Params)
	require.NoError(t, err, "failed to query token transactions page 2")

	// Verify we got exactly 1 transaction
	require.Len(t, tokenTransactionsPage2.TokenTransactionsWithStatus, 1, "expected 1 token transaction in page 2")

	// Verify the offset is 2 (indicating there are more results)
	require.Equal(t, int64(2), tokenTransactionsPage2.Offset, "expected next offset 2 for page 2")

	// Second transaction should be the mint (reverse chronological)
	mintTx := tokenTransactionsPage2.TokenTransactionsWithStatus[0].TokenTransaction
	require.NotNil(t, mintTx.GetMintInput(), "second transaction should be a mint transaction")
	require.Equal(t, tokenPrivKey.Public().Serialize(), mintTx.GetMintInput().GetIssuerPublicKey(), "mint transaction issuer public key does not match expected")

	// Test QueryTokenTransactionsNative with pagination - third page (should be empty)
	page3Params := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  []keys.Public{tokenPrivKey.Public()},
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: nil,
		Offset:            tokenTransactionsPage2.Offset,
		Limit:             1,
	}
	tokenTransactionsPage3, err := wallet.QueryTokenTransactionsV2(t.Context(), config, page3Params)
	require.NoError(t, err, "failed to query token transactions page 3")

	// Verify we got no transactions
	require.Empty(t, tokenTransactionsPage3.TokenTransactionsWithStatus, "expected 0 token transactions in page 3")

	// Verify the offset is -1 (indicating end of results)
	require.Equal(t, int64(-1), tokenTransactionsPage3.Offset, "expected next offset -1 for page 3")

	// Validate transfer transaction details
	require.Len(t, transferTx.TokenOutputs, 1, "expected 1 created output in transfer transaction")
	transferAmount = new(big.Int).SetBytes(transferTx.TokenOutputs[0].TokenAmount)
	require.Equal(t, 0, transferAmount.Cmp(expectedTransferAmount), "transfer amount does not match expected")
	require.Equal(t, userOutput3PubKeyBytes, transferTx.TokenOutputs[0].OwnerPublicKey, "transfer created output owner public key does not match expected")

	// Validate mint transaction details
	require.Len(t, mintTx.TokenOutputs, 2, "expected 2 created outputs in mint transaction")
	userOutput1Pubkey := userOutput1PrivKey.Public().Serialize()
	userOutput2Pubkey := userOutput2PrivKey.Public().Serialize()

	if bytes.Equal(mintTx.TokenOutputs[0].OwnerPublicKey, userOutput1Pubkey) {
		require.Equal(t, mintTx.TokenOutputs[1].OwnerPublicKey, userOutput2Pubkey)
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[0].TokenAmount), uint64ToBigInt(TestIssueOutput1Amount))
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[1].TokenAmount), uint64ToBigInt(TestIssueOutput2Amount))
	} else if bytes.Equal(mintTx.TokenOutputs[0].OwnerPublicKey, userOutput2Pubkey) {
		require.Equal(t, mintTx.TokenOutputs[1].OwnerPublicKey, userOutput1Pubkey)
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[0].TokenAmount), uint64ToBigInt(TestIssueOutput2Amount))
		require.Equal(t, bytesToBigInt(mintTx.TokenOutputs[1].TokenAmount), uint64ToBigInt(TestIssueOutput1Amount))
	} else {
		t.Fatalf("mint transaction output keys (%x, %x) do not match expected (%x, %x)",
			mintTx.TokenOutputs[0].OwnerPublicKey,
			mintTx.TokenOutputs[1].OwnerPublicKey,
			userOutput1Pubkey,
			userOutput2Pubkey,
		)
	}
}

func TestCoordinatedTokenMintAndTransferTokensTooManyOutputsFails(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	tokenPrivKey := config.IdentityPrivateKey
	tooBigIssuanceTransaction, _, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
		tokenPrivKey.Public(), utils.MaxInputOrOutputTokenTransactionOutputs+1)
	require.NoError(t, err, "failed to create test token issuance transaction")

	_, err = wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, tooBigIssuanceTransaction, []keys.Private{tokenPrivKey},
	)
	require.Error(t, err, "expected error when broadcasting issuance transaction with more than utils.MaxInputOrOutputTokenTransactionOutputs=%d outputs", utils.MaxInputOrOutputTokenTransactionOutputs)
}

// TestCoordinatedTokenMintAndTransferTokensLotsOfOutputs tests the coordinated flow with many outputs
func TestCoordinatedTokenMintAndTransferTokensWithTooManyInputsFails(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
	tokenPrivKey := config.IdentityPrivateKey
	// Create first issuance transaction with MAX outputs
	issueTokenTransactionFirstBatch, userOutputPrivKeysFirstBatch, err := createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t, config,
		tokenPrivKey.Public(), maxInputOrOutputTokenTransactionOutputsForTests)
	require.NoError(t, err, "failed to create test token issuance transaction")

	finalIssueTokenTransactionFirstBatch, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, issueTokenTransactionFirstBatch, []keys.Private{tokenPrivKey},
	)
	require.NoError(t, err, "failed to broadcast issuance token transaction")

	// Create second issuance transaction with MAX outputs
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

	// Create consolidation transaction
	consolidatedOutputPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")

	consolidatedOutputPubKeyBytes := consolidatedOutputPrivKey.Public().Serialize()

	// Create a transfer transaction that consolidates all outputs with too many inputs.
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

	// Broadcast the consolidation transaction
	_, err = wallet.BroadcastCoordinatedTokenTransfer(t.Context(), config, tooManyTransaction, allUserOutputPrivKeys)
	require.Error(t, err, "expected error when broadcasting transfer transaction with more than MaxInputOrOutputTokenTransactionOutputsForTests=%d inputs", maxInputOrOutputTokenTransactionOutputsForTests)
}

func TestCoordinatedTokenMintAndTransferMaxInputsSucceeds(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

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

	consolidatedOutputPrivKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")
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

// TestCoordinatedNativeTokenMaxSupplyEnforcement tests that max supply is properly enforced for both L1 and native spark tokens
func TestCoordinatedNativeTokenMaxSupplyEnforcement(t *testing.T) {
	testCases := []struct {
		name                 string
		maxSupply            uint64
		mintAmounts          []uint64
		startExtraMintBefore bool
		expectedResults      []bool // true = should succeed, false = should fail
	}{
		{
			name:            "mints should fail if exceeding max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{500, 600}, // 500 + 600 = 1100 > 1000
			expectedResults: []bool{true, false},
		},
		{
			name:            "mints should succeed if within max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{400, 500}, // 400 + 500 = 900 <= 1000
			expectedResults: []bool{true, true},
		},
		{
			name:            "mints should succeed if exactly matching max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{600, 400}, // exactly 1000
			expectedResults: []bool{true, true},
		},
		{
			name:            "mints should succeed if has unlimited max supply",
			maxSupply:       0, // unlimited
			mintAmounts:     []uint64{1000000, 2000000},
			expectedResults: []bool{true, true},
		},
		{
			name:            "mints should fail if single mint exceeds max supply",
			maxSupply:       1000,
			mintAmounts:     []uint64{1001}, // exceeds max supply in single transaction
			expectedResults: []bool{false},
		},
		{
			name:                 "mints should still succeed if mint started before",
			maxSupply:            1000,
			mintAmounts:          []uint64{1000},
			expectedResults:      []bool{true},
			startExtraMintBefore: true,
		},
	}

	for _, tc := range testCases {
		config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

		// Create native spark token with specified max supply
		tokenPrivKey := getRandomPrivateKey(t)
		err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
			IssuerPrivateKey: tokenPrivKey,
			Name:             "MaxTest",
			Ticker:           "MAXT",
			MaxSupply:        tc.maxSupply,
		})
		require.NoError(t, err, "failed to create native spark token with max supply %d", tc.maxSupply)

		// Test each mint amount
		for i, mintAmount := range tc.mintAmounts {
			expectedResult := tc.expectedResults[i]

			// Create mint transaction using utility function
			mintTransaction, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: tokenPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          1,
				OutputAmounts:       []uint64{mintAmount},
			})
			require.NoError(t, err, "failed to create mint transaction %d", i+1)

			if tc.startExtraMintBefore {
				// Ensure that starting but not signing a mint does not interfere with max supply
				// computation on a subsequent mint even if together they would exceed the max supply.
				mintTransaction.ClientCreatedTimestamp = timestamppb.New(time.Now().Add(-time.Second))
				_, _, err = wallet.StartTokenTransactionCoordinated(
					t.Context(),
					config,
					mintTransaction,
					[]keys.Private{tokenPrivKey},
					TestValidityDurationSecs,
					nil,
				)
				require.NoError(t, err, "failed to start mint transaction before")
			}

			// Test mint transaction
			_, err = wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, mintTransaction,
				[]keys.Private{tokenPrivKey},
			)

			if expectedResult {
				require.NoError(t, err, "mint %d of %d tokens should succeed (total so far: %d, max supply: %d)",
					i+1, mintAmount, sumUint64Slice(tc.mintAmounts[:i+1]), tc.maxSupply)
			} else {
				require.Error(t, err, "mint %d of %d tokens should fail (total would be: %d, max supply: %d)",
					i+1, mintAmount, sumUint64Slice(tc.mintAmounts[:i+1]), tc.maxSupply)
				require.ErrorContains(t, err, "max supply", "error should mention max supply")
			}
		}
	}
}

// Helper function to sum a slice of uint64 values
func sumUint64Slice(values []uint64) uint64 {
	var sum uint64
	for _, v := range values {
		sum += v
	}
	return sum
}

func TestV1FreezeAndUnfreezeTokens(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			// Broadcast the token transaction
			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			for i, output := range finalIssueTokenTransaction.TokenOutputs {
				if output.GetWithdrawBondSats() != WithdrawalBondSatsInConfig {
					t.Errorf("output %d: expected withdrawal bond sats %d, got %d", i, uint64(WithdrawalBondSatsInConfig), output.GetWithdrawBondSats())
				}
				if output.GetWithdrawRelativeBlockLocktime() != uint64(WithdrawalRelativeBlockLocktimeInConfig) {
					t.Errorf("output %d: expected withdrawal relative block locktime %d, got %d", i, uint64(WithdrawalRelativeBlockLocktimeInConfig), output.GetWithdrawRelativeBlockLocktime())
				}
			}

			// Call FreezeTokens to freeze the created output
			ownerPubKey, err := keys.ParsePublicKey(finalIssueTokenTransaction.TokenOutputs[0].OwnerPublicKey)
			require.NoError(t, err)
			freezeResponse, err := wallet.FreezeTokensV1(t.Context(), config, ownerPubKey, finalIssueTokenTransaction.TokenOutputs[0].TokenIdentifier, false)
			require.NoError(t, err, "failed to freeze tokens")

			// Convert frozen amount bytes to big.Int for comparison
			frozenAmount := new(big.Int).SetBytes(freezeResponse.ImpactedTokenAmount)

			// Calculate total amount from transaction created outputs
			expectedAmount := new(big.Int).SetBytes(int64ToUint128Bytes(0, TestIssueOutput1Amount))
			expectedOutputID := finalIssueTokenTransaction.TokenOutputs[0].Id

			require.Equal(t, expectedAmount, frozenAmount,
				"frozen amount %s does not match expected amount %s", frozenAmount.String(), expectedAmount.String())
			require.Len(t, freezeResponse.ImpactedOutputIds, 1, "expected 1 impacted output ID")
			require.Equal(t, *expectedOutputID, freezeResponse.ImpactedOutputIds[0],
				"frozen output ID %s does not match expected output ID %s", freezeResponse.ImpactedOutputIds[0], *expectedOutputID)

			finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
			require.NoError(t, err, "failed to hash final transfer token transaction")

			// Create transfer transaction
			transferTokenTransaction, _, err := createTestTokenTransferTransaction(config, finalIssueTokenTransactionHash, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token transfer transaction")

			// Convert to tokenpb for the coordinated API
			tokenTransferTokenTransaction, err := protoconverter.TokenProtoFromSparkTokenTransaction(transferTokenTransaction)
			require.NoError(t, err, "failed to convert transfer token transaction")

			// Broadcast the token transaction (should fail due to frozen tokens)
			transferFrozenTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, tokenTransferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.Error(t, err, "expected error when transferring frozen tokens")
			require.Nil(t, transferFrozenTokenTransactionResponse, "expected nil response when transferring frozen tokens")

			// Call FreezeTokens to thaw the created output
			unfreezeResponse, err := wallet.FreezeTokensV1(t.Context(), config, ownerPubKey, finalIssueTokenTransaction.TokenOutputs[0].TokenIdentifier, true)
			require.NoError(t, err, "failed to unfreeze tokens")

			// Convert frozen amount bytes to big.Int for comparison
			thawedAmount := new(big.Int).SetBytes(unfreezeResponse.ImpactedTokenAmount)

			require.Equal(t, expectedAmount, thawedAmount,
				"thawed amount %s does not match expected amount %s", thawedAmount.String(), expectedAmount.String())
			require.Len(t, unfreezeResponse.ImpactedOutputIds, 1, "expected 1 impacted output ID")
			require.Equal(t, *expectedOutputID, unfreezeResponse.ImpactedOutputIds[0],
				"thawed output ID %s does not match expected output ID %s", unfreezeResponse.ImpactedOutputIds[0], *expectedOutputID)

			// Broadcast the token transaction (should succeed now that tokens are thawed)
			transferTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, tokenTransferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast thawed token transaction")
			require.NotNil(t, transferTokenTransactionResponse, "expected non-nil response when transferring thawed tokens")
		})
	}
}

// TestCoordinatedBroadcastTokenTransactionWithInvalidPrevTxHash tests validation with invalid transaction hashes
func TestCoordinatedBroadcastTokenTransactionWithInvalidPrevTxHash(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
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

			// Corrupt the transaction hash by adding a byte
			corruptedHash := append(finalIssueTokenTransactionHash, 0xFF)

			// Create transfer transaction with corrupted hash
			transferTokenTransaction := &tokenpb.TokenTransaction{
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: corruptedHash, // Corrupted hash
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
						TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
					},
				},
				Network:                         config.ProtoNetwork(),
				SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
			}

			// Attempt to broadcast the transfer transaction with corrupted hash
			// This should fail validation
			_, err = wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)

			require.Error(t, err, "expected transaction with invalid hash to be rejected")
		})
	}
}

// TestCoordinatedBroadcastTokenTransactionUnspecifiedNetwork tests validation with unspecified network
func TestCoordinatedBroadcastTokenTransactionUnspecifiedNetwork(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
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
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = wallet.BroadcastCoordinatedTokenTransferWithExpiryDuration(
				t.Context(), config, issueTokenTransaction, TooLongValidityDurationSecs,
				[]keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction with too long validity duration to be rejected")
		})
	}
}

func TestCoordinatedBroadcastTokenTransactionTooShortValidityDuration(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, _, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")
			issueTokenTransaction.Network = sparkpb.Network_UNSPECIFIED

			_, err = wallet.BroadcastCoordinatedTokenTransferWithExpiryDuration(
				t.Context(), config, issueTokenTransaction, TooShortValidityDurationSecs, []keys.Private{tokenPrivKey},
			)

			require.Error(t, err, "expected transaction with 0 validity duration to be rejected")
		})
	}
}

// TestCoordinatedQueryTokenOutputsByNetworkReturnsNoneForMismatchedNetwork tests network filtering
func TestCoordinatedQueryTokenOutputsByNetworkReturnsNoneForMismatchedNetwork(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			tokenPrivKey := config.IdentityPrivateKey
			issueTokenTransaction, userOutput1PrivKey, _, err := createTestTokenMintTransactionTokenPb(t, config, tokenPrivKey.Public())
			require.NoError(t, err, "failed to create test token issuance transaction")

			_, err = wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction, []keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			userOneConfig := sparktesting.TestWalletConfigWithIdentityKey(t, userOutput1PrivKey)

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

func createTestTokenMintTransactionTokenPbWithParams(t *testing.T, config *wallet.TestWalletConfig, params tokenTransactionParams) (*tokenpb.TokenTransaction, []keys.Private, error) {
	// Set defaults for backward compatibility
	numOutputs := params.NumOutputs
	if numOutputs == 0 {
		numOutputs = 2
	}

	// OutputAmounts must be provided and match NumOutputs
	if len(params.OutputAmounts) == 0 {
		return nil, nil, fmt.Errorf("OutputAmounts must be provided and cannot be empty")
	}
	if len(params.OutputAmounts) != numOutputs {
		return nil, nil, fmt.Errorf("OutputAmounts length (%d) must match NumOutputs (%d)", len(params.OutputAmounts), numOutputs)
	}

	outputAmounts := params.OutputAmounts

	// Generate private keys and create outputs
	userOutputPrivKeys := make([]keys.Private, numOutputs)
	tokenOutputs := make([]*tokenpb.TokenOutput, numOutputs)

	for i := 0; i < numOutputs; i++ {
		privKey, err := keys.GeneratePrivateKey()
		if err != nil {
			return nil, nil, err
		}
		userOutputPrivKeys[i] = privKey
		pubKeyBytes := privKey.Public().Serialize()
		if params.MintToSelf {
			pubKeyBytes = params.TokenIdentityPubKey.Serialize()
			userOutputPrivKeys[i] = config.IdentityPrivateKey
		}
		tokenOutputs[i] = &tokenpb.TokenOutput{
			OwnerPublicKey: pubKeyBytes,
			TokenPublicKey: params.TokenIdentityPubKey.Serialize(),
			TokenAmount:    int64ToUint128Bytes(0, outputAmounts[i]),
		}
	}

	now := time.Now()
	version := uint32(TokenTransactionVersion2)
	if params.Version != 0 {
		version = uint32(params.Version)
	}
	mintTokenTransaction := &tokenpb.TokenTransaction{
		Version: version,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: params.TokenIdentityPubKey.Serialize(),
			},
		},
		TokenOutputs:                    tokenOutputs,
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		ClientCreatedTimestamp:          timestamppb.New(now),
	}

	if version >= 3 {
		sort.Slice(mintTokenTransaction.SparkOperatorIdentityPublicKeys, func(i, j int) bool {
			return bytes.Compare(mintTokenTransaction.SparkOperatorIdentityPublicKeys[i], mintTokenTransaction.SparkOperatorIdentityPublicKeys[j]) < 0
		})
	}

	if params.UseTokenIdentifier {
		tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, params.TokenIdentityPubKey)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get token identifier from metadata: %w", err)
		}
		mintTokenTransaction.GetMintInput().TokenIdentifier = tokenIdentifier
		for _, output := range mintTokenTransaction.TokenOutputs {
			output.TokenIdentifier = tokenIdentifier
			output.TokenPublicKey = nil
		}
	}

	return mintTokenTransaction, userOutputPrivKeys, nil
}

func createTestTokenMintTransactionTokenPb(t *testing.T, config *wallet.TestWalletConfig, tokenIdentityPubKey keys.Public) (*tokenpb.TokenTransaction, keys.Private, keys.Private, error) {
	tx, privKeys, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: tokenIdentityPubKey,
		IsNativeSparkToken:  false,
		UseTokenIdentifier:  true,
		NumOutputs:          2,
		OutputAmounts:       []uint64{uint64(TestIssueOutput1Amount), uint64(TestIssueOutput2Amount)},
	})
	if err != nil {
		return nil, keys.Private{}, keys.Private{}, err
	}
	if len(privKeys) != 2 {
		return nil, keys.Private{}, keys.Private{}, fmt.Errorf("expected 2 private keys, got %d", len(privKeys))
	}
	return tx, privKeys[0], privKeys[1], nil
}

func createTestTokenTransferTransactionTokenPbWithParams(t *testing.T, config *wallet.TestWalletConfig, params tokenTransactionParams) (*tokenpb.TokenTransaction, keys.Private, error) {
	userOutput3PrivKey, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, keys.Private{}, err
	}
	userOutput3PubKeyBytes := userOutput3PrivKey.Public().Serialize()

	version := uint32(TokenTransactionVersion2)
	if params.Version != 0 {
		version = uint32(params.Version)
	}
	transferTokenTransaction := &tokenpb.TokenTransaction{
		Version: version,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{
					{
						PrevTokenTransactionHash: params.FinalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 0,
					},
					{
						PrevTokenTransactionHash: params.FinalIssueTokenTransactionHash,
						PrevTokenTransactionVout: 1,
					},
				},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey: userOutput3PubKeyBytes,
				TokenPublicKey: params.TokenIdentityPubKey.Serialize(),
				TokenAmount:    int64ToUint128Bytes(0, TestTransferOutput1Amount),
			},
		},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		ClientCreatedTimestamp:          timestamppb.New(time.Now()),
		InvoiceAttachments:              params.InvoiceAttachments,
	}

	if version >= 3 {
		sort.Slice(transferTokenTransaction.SparkOperatorIdentityPublicKeys, func(i, j int) bool {
			return bytes.Compare(transferTokenTransaction.SparkOperatorIdentityPublicKeys[i], transferTokenTransaction.SparkOperatorIdentityPublicKeys[j]) < 0
		})
	}

	if params.UseTokenIdentifier {
		tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, params.TokenIdentityPubKey)
		if err != nil {
			return nil, keys.Private{}, fmt.Errorf("failed to get token identifier from metadata: %w", err)
		}
		transferTokenTransaction.TokenOutputs[0].TokenIdentifier = tokenIdentifier
		transferTokenTransaction.TokenOutputs[0].TokenPublicKey = nil
	}
	return transferTokenTransaction, userOutput3PrivKey, nil
}

func createTestTokenTransferTransactionTokenPb(
	t *testing.T,
	config *wallet.TestWalletConfig,
	finalIssueTokenTransactionHash []byte,
	tokenIdentityPubKey keys.Public,
) (*tokenpb.TokenTransaction, keys.Private, error) {
	return createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            tokenIdentityPubKey,
		IsNativeSparkToken:             false,
		UseTokenIdentifier:             true,
		FinalIssueTokenTransactionHash: finalIssueTokenTransactionHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(TestTransferOutput1Amount)},
	})
}

func createTestTokenMintTransactionWithMultipleTokenOutputsTokenPb(t *testing.T,
	config *wallet.TestWalletConfig,
	tokenIdentityPubKey keys.Public, numOutputs int,
) (*tokenpb.TokenTransaction, []keys.Private, error) {
	// Create an array that evenly distributes the amount
	outputAmounts := make([]uint64, numOutputs)
	for i := 0; i < numOutputs; i++ {
		outputAmounts[i] = uint64(testIssueMultiplePerOutputAmount)
	}

	return createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: tokenIdentityPubKey,
		IsNativeSparkToken:  false,
		UseTokenIdentifier:  true,
		NumOutputs:          numOutputs,
		OutputAmounts:       outputAmounts,
	})
}

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
	// -------------------- STEP 1: START --------------------
	converted := make([]keys.Private, len(startOwnerPrivateKeys))
	for i, key := range startOwnerPrivateKeys {
		converted[i] = key
	}
	startResp, finalTxHash, startErr := wallet.StartTokenTransactionCoordinated(
		t.Context(), config, tokenTransaction, converted, TestValidityDurationSecs, nil,
	)

	if expectedStartError {
		require.Error(t, startErr, "expected start error but none")
		return nil
	} else {
		require.NoError(t, startErr, "unexpected start error")
	}

	if doubleStartSameTx {
		startResp2, finalTxHash2, startErr2 := wallet.StartTokenTransactionCoordinated(
			t.Context(), config, tokenTransaction, converted, TestValidityDurationSecs, nil,
		)
		require.NoError(t, startErr2, "unexpected error on second start")
		hash1, _ := utils.HashTokenTransaction(startResp.FinalTokenTransaction, false)
		hash2, _ := utils.HashTokenTransaction(startResp2.FinalTokenTransaction, false)
		require.Equal(t, finalTxHash, finalTxHash2, "final tx hashes should match on double start")
		require.Equal(t, hash1, hash2, "final transactions should hash identically after double start with same tx blob")
	}

	if doubleStartDifferentTx {
		// Change client created timestamp to be earlier so as to not trigger idempotency logic but also have this second
		// broadcasted transaction succeed.
		tokenTransaction.ClientCreatedTimestamp = timestamppb.New(tokenTransaction.ClientCreatedTimestamp.AsTime().Add(-time.Second * 1))
		startResp2, finalTxHash2, startErr2 := wallet.StartTokenTransactionCoordinated(
			t.Context(), config, tokenTransaction, converted, TestValidityDurationSecs, nil,
		)
		require.NoError(t, startErr2, "unexpected error on second start")
		hash1, _ := utils.HashTokenTransaction(startResp.FinalTokenTransaction, false)
		hash2, _ := utils.HashTokenTransaction(startResp2.FinalTokenTransaction, false)
		require.NotEqual(t, finalTxHash, finalTxHash2, "final tx hashes should not match when double starting with different txs")
		require.NotEqual(t, hash1, hash2, "final transactions should hash differently for txs with different client created timestamp")

		// Ensure that despite remapping the inputs to the new TX, that both token transactions are still queryable by the client.
		txQueryParams := wallet.QueryTokenTransactionsParams{
			IssuerPublicKeys:  []keys.Public{},
			OwnerPublicKeys:   nil,
			OutputIDs:         nil,
			TransactionHashes: [][]byte{finalTxHash, finalTxHash2},
			Offset:            0,
			Limit:             2,
		}
		txQueryResponse, err := wallet.QueryTokenTransactionsV2(
			t.Context(),
			config,
			txQueryParams,
		)
		require.NoError(t, err, "failed to query token transactions")
		require.Len(t, txQueryResponse.TokenTransactionsWithStatus, 2)

		// Validate pre-emption logic: both transactions should be queryable.
		txType, _ := utils.InferTokenTransactionType(tokenTransaction)
		var winnerFound, loserFound bool
		if txType == utils.TokenTransactionTypeTransfer {
			for _, txWithStatus := range txQueryResponse.TokenTransactionsWithStatus {
				if bytes.Equal(txWithStatus.TokenTransactionHash, finalTxHash2) {
					// This should be the second transaction (winner)
					require.NotNil(t, txWithStatus.TokenTransaction.GetTransferInput(), "winning transaction should have transfer input")
					require.NotEmpty(t, txWithStatus.TokenTransaction.GetTransferInput().GetOutputsToSpend(), "winning transaction should have outputs to spend")
					winnerFound = true
				} else if bytes.Equal(txWithStatus.TokenTransactionHash, finalTxHash) {
					// This should be the first transaction (pre-empted)
					// The pre-empted transaction (if a transfer) should not have inputs because they were re-mapped to the winner.
					require.Nil(t, txWithStatus.TokenTransaction.GetTransferInput(), "winning transaction should have transfer input")
					loserFound = true
				}
			}
			require.True(t, winnerFound, "winner transaction should be found")
			require.True(t, loserFound, "loser transaction should be found")
		}

		// Use the second start response for the commit.
		startResp = startResp2
		finalTxHash = finalTxHash2
	}

	if expiredCommit {
		wait := time.Duration(TestValidityDurationSecsPlus1) * time.Second
		t.Logf("Waiting %v for transaction expiry", wait)
		time.Sleep(wait)
	}

	// -------------------- STEP 2: COMMIT --------------------
	var operatorSignatures []*tokenpb.InputTtxoSignaturesPerOperator
	for _, operator := range config.SigningOperators {
		var ttxoSigs []*tokenpb.SignatureWithIndex
		for idx, privKey := range commitOwnerPrivateKeys {
			payload := &sparkpb.OperatorSpecificTokenTransactionSignablePayload{
				FinalTokenTransactionHash: finalTxHash,
				OperatorIdentityPublicKey: operator.IdentityPublicKey.Serialize(),
			}
			payloadHash, hashErr := utils.HashOperatorSpecificTokenTransactionSignablePayload(payload)
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

	commitResp, commitErr := wallet.CommitTransactionCoordinated(t.Context(), config, commitReq)

	if expectedCommitError {
		require.Error(t, commitErr, "expected error during commit but none")
		return nil
	}
	require.NoError(t, commitErr)

	require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, commitResp.CommitStatus)
	require.Nil(t, commitResp.CommitProgress, "commit progress should be nil")

	if doubleCommit {
		// Try committing again to simulate double sign
		commitResp2, commitErr2 := wallet.CommitTransactionCoordinated(t.Context(), config, commitReq)
		require.NoError(t, commitErr2, "unexpected error on second commit (double sign)")
		require.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, commitResp2.CommitStatus)
	}

	return startResp.FinalTokenTransaction
}

func testCoordinatedCreateNativeSparkTokenWithParams(t *testing.T, config *wallet.TestWalletConfig, params createNativeSparkTokenParams) error {
	createTx, err := createTestCoordinatedTokenCreateTransactionWithParams(config, params)
	if err != nil {
		return err
	}
	_, err = wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(),
		config,
		createTx,
		[]keys.Private{params.IssuerPrivateKey},
	)
	return err
}

func createTestCoordinatedTokenCreateTransactionWithParams(config *wallet.TestWalletConfig, params createNativeSparkTokenParams) (*tokenpb.TokenTransaction, error) {
	issuerPubKeyBytes := params.IssuerPrivateKey.Public().Serialize()
	createTokenTransaction := &tokenpb.TokenTransaction{
		Version: TokenTransactionVersion2,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: issuerPubKeyBytes,
				TokenName:       params.Name,
				TokenTicker:     params.Ticker,
				Decimals:        TestTokenDecimals,
				MaxSupply:       getTokenMaxSupplyBytes(params.MaxSupply),
				IsFreezable:     TestTokenIsFreezable,
			},
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{},
		Network:                         config.ProtoNetwork(),
		SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
		ClientCreatedTimestamp:          timestamppb.New(time.Now()),
	}
	return createTokenTransaction, nil
}

// Helper function to verify individual token metadata entries
func verifyTokenMetadata(t *testing.T, metadata *tokenpb.TokenMetadata, expectedParams sparkTokenCreationTestParams, queryMethod string) {
	issuerPublicKey := expectedParams.issuerPrivateKey.Public().Serialize()
	require.Equal(t, expectedParams.name, metadata.TokenName, "%s: token name should match, expected: %s, found: %s", queryMethod, expectedParams.name, metadata.TokenName)
	require.Equal(t, expectedParams.ticker, metadata.TokenTicker, "%s: token ticker should match, expected: %s, found: %s", queryMethod, expectedParams.ticker, metadata.TokenTicker)
	require.Equal(t, uint32(TestTokenDecimals), metadata.Decimals, "%s: token decimals should match, expected: %d, found: %d", queryMethod, uint32(TestTokenDecimals), metadata.Decimals)
	require.Equal(t, TestTokenIsFreezable, metadata.IsFreezable, "%s: token freezable flag should match, expected: %t, found: %t", queryMethod, TestTokenIsFreezable, metadata.IsFreezable)
	require.True(t, bytes.Equal(issuerPublicKey, metadata.IssuerPublicKey), "%s: issuer public key should match, expected: %x, found: %x", queryMethod, issuerPublicKey, metadata.IssuerPublicKey)
	require.True(t, bytes.Equal(getTokenMaxSupplyBytes(expectedParams.maxSupply), metadata.MaxSupply), "%s: max supply should match, expected: %x, found: %x", queryMethod, getTokenMaxSupplyBytes(expectedParams.maxSupply), metadata.MaxSupply)
}

// Helper function to create a native token (no verification)
func createNativeToken(t *testing.T, params sparkTokenCreationTestParams) error {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, params.issuerPrivateKey)

	return testCoordinatedCreateNativeSparkTokenWithParams(t,
		config,
		createNativeSparkTokenParams{
			IssuerPrivateKey: params.issuerPrivateKey,
			Name:             params.name,
			Ticker:           params.ticker,
			MaxSupply:        params.maxSupply,
		})
}

// Helper function to verify a token exists and return its identifier
func verifyNativeToken(t *testing.T, params sparkTokenCreationTestParams) []byte {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, params.issuerPrivateKey)

	// Verify we can query the token
	issuerPubKey := params.issuerPrivateKey.Public()
	resp, err := wallet.QueryTokenMetadata(t.Context(), config, nil, []keys.Public{issuerPubKey})
	require.NoError(t, err, "failed to query created token metadata")
	require.Len(t, resp.TokenMetadata, 1, "expected exactly 1 token metadata entry")

	return resp.TokenMetadata[0].TokenIdentifier
}

// Helper function to verify token metadata using comprehensive checks (for metadata-focused tests)
func queryAndVerifyTokenMetadata(t *testing.T, config *wallet.TestWalletConfig, params sparkTokenCreationTestParams) {
	issuerPublicKey := params.issuerPrivateKey.Public()

	// Test 1: Query by issuer public key
	resp1, err := wallet.QueryTokenMetadata(t.Context(), config, nil, []keys.Public{issuerPublicKey})
	require.NoError(t, err, "failed to query token metadata by issuer public key")

	require.NotNil(t, resp1)
	require.Len(t, resp1.TokenMetadata, 1)

	metadata1 := resp1.TokenMetadata[0]
	verifyTokenMetadata(t, metadata1, params, "issuer public key query")

	// Test 2: Query by token identifier (using the one from the first query)
	tokenIdentifier := metadata1.TokenIdentifier
	resp2, err := wallet.QueryTokenMetadata(t.Context(), config, [][]byte{tokenIdentifier}, nil)
	require.NoError(t, err, "failed to query token metadata by token identifier")
	require.NotNil(t, resp2, "token metadata response should not be nil")
	require.Len(t, resp2.TokenMetadata, 1)

	metadata2 := resp2.TokenMetadata[0]
	verifyTokenMetadata(t, metadata2, params, "token identifier query")

	// Verify the token identifiers match
	require.Equal(t, metadata1.TokenIdentifier, metadata2.TokenIdentifier, "token identifier should be identical across queries")
}

// queryAndVerifyTokenOutputs verifies the token outputs from the given finalTokenTransaction assigned to the owner private key are queryable
// for the given coordinator identifiers.
func queryAndVerifyTokenOutputs(t *testing.T, coordinatorIdentifiers []string, finalTokenTransaction *tokenpb.TokenTransaction, ownerPrivateKey keys.Private) {
	ownerPubKeyBytes := ownerPrivateKey.Public().Serialize()
	var expectedOutputs []*tokenpb.TokenOutput
	for _, output := range finalTokenTransaction.TokenOutputs {
		if bytes.Equal(output.OwnerPublicKey, ownerPubKeyBytes) {
			expectedOutputs = append(expectedOutputs, output)
		}
	}

	for _, coordinatorIdentifier := range coordinatorIdentifiers {
		config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
		config.CoordinatorIdentifier = coordinatorIdentifier

		outputs, err := wallet.QueryTokenOutputsV2(t.Context(), config, []keys.Public{ownerPrivateKey.Public()}, nil)
		require.NoError(t, err, "failed to query token outputs from coordinator: %s", coordinatorIdentifier)
		require.Len(t, outputs.OutputsWithPreviousTransactionData, len(expectedOutputs), "expected %d outputs from coordinator: %s", len(expectedOutputs), coordinatorIdentifier)

		for j, expectedOutput := range expectedOutputs {
			require.Equal(t, expectedOutput.Id, outputs.OutputsWithPreviousTransactionData[j].Output.Id, "expected the same output ID for output %d from coordinator: %s", j, coordinatorIdentifier)
		}
	}
}

// queryAndVerifyNoTokenOutputs verifies that no token outputs are queryable for the ownerPrivateKey for any of the given
// coordinator identifiers.
func queryAndVerifyNoTokenOutputs(t *testing.T, coordinatorIdentifiers []string, ownerPrivateKey keys.Private) {
	queryAndVerifyTokenOutputs(t, coordinatorIdentifiers, &tokenpb.TokenTransaction{TokenOutputs: []*tokenpb.TokenOutput{}}, ownerPrivateKey)
}

// getTokenIdentifierFromMetadata retrieves token identifier by querying token metadata
func getTokenIdentifierFromMetadata(ctx context.Context, config *wallet.TestWalletConfig, issuerPubKey keys.Public) ([]byte, error) {
	response, err := wallet.QueryTokenMetadata(
		ctx,
		config,
		nil,                         // tokenIdentifiers
		[]keys.Public{issuerPubKey}, // issuerPublicKeys
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query token metadata: %w", err)
	}

	if len(response.TokenMetadata) == 0 {
		return nil, fmt.Errorf("no token metadata found for issuer public key")
	}

	return response.TokenMetadata[0].TokenIdentifier, nil
}

func TestQueryTokenMetadataWithNoParams(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	// Ensure calling with no params returns an error
	_, err := wallet.QueryTokenMetadata(t.Context(), config, nil, nil)
	require.Error(t, err, "calling query token metadata with no params should return an error")
}

func TestQueryTokenMetadataL1Token(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	// Verify the L1 token metadata using the helper function
	l1TokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: config.IdentityPrivateKey,
		name:             TestTokenName,
		ticker:           TestTokenTicker,
		maxSupply:        TestTokenMaxSupply,
		// Don't specify a creation entity key to validate for L1 token creation. It will be set to the SOs entity DKG key,
		// but because this token was created automatically by scanning L1, we don't know exactly what it is ahead of time.
		expectedError: false,
	}
	queryAndVerifyTokenMetadata(t, config, l1TokenParams)
}

func TestQueryTokenMetadataNativeSparkToken(t *testing.T) {
	// Create a native Spark token using a random key
	nativeTokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: getRandomPrivateKey(t),
		name:             "Native Test Token",
		ticker:           "NATIVE",
		maxSupply:        5000000,
	}

	err := createNativeToken(t, nativeTokenParams)
	require.NoError(t, err, "failed to create native token")

	config := sparktesting.TestWalletConfigWithIdentityKey(t, nativeTokenParams.issuerPrivateKey)
	require.NoError(t, err, "failed to create wallet config")

	// Verify the native token metadata using comprehensive verification
	queryAndVerifyTokenMetadata(t, config, nativeTokenParams)
}

func TestQueryTokenMetadataMixedParams(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	// Create a native Spark token using the helper
	nativeTokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: getRandomPrivateKey(t),
		name:             "Native Token",
		ticker:           "NATIV",
		maxSupply:        1000000,
	}
	err := createNativeToken(t, nativeTokenParams)
	require.NoError(t, err, "failed to create native token")
	nativeTokenIdentifier := verifyNativeToken(t, nativeTokenParams)

	// L1 token uses the config's identity key as issuer
	l1TokenIssuerPubKey := config.IdentityPrivateKey.Public()

	// Test: Query for both tokens using mixed parameters in a single call
	// - Native token by its token identifier
	// - L1 token by its issuer public key
	mixedResp, err := wallet.QueryTokenMetadata(
		t.Context(),
		config,
		[][]byte{nativeTokenIdentifier},    // Token identifiers
		[]keys.Public{l1TokenIssuerPubKey}, // Issuer public keys
	)
	require.NoError(t, err, "failed to query token metadata with mixed parameters")
	require.Len(t, mixedResp.TokenMetadata, 2)

	// Verify we got both tokens back using the helper function
	var foundNativeToken, foundL1Token bool
	l1TokenParams := sparkTokenCreationTestParams{
		issuerPrivateKey: config.IdentityPrivateKey,
		name:             TestTokenName,
		ticker:           TestTokenTicker,
		maxSupply:        TestTokenMaxSupply,
	}

	for _, metadata := range mixedResp.TokenMetadata {
		if bytes.Equal(metadata.TokenIdentifier, nativeTokenIdentifier) {
			foundNativeToken = true
			verifyTokenMetadata(t, metadata, nativeTokenParams, "mixed query - native token")
		} else if issuerPubKey, err := keys.ParsePublicKey(metadata.IssuerPublicKey); err == nil && issuerPubKey.Equals(l1TokenIssuerPubKey) {
			foundL1Token = true
			verifyTokenMetadata(t, metadata, l1TokenParams, "mixed query - L1 token")
		}
	}

	require.True(t, foundNativeToken, "native token should be found in mixed query results")
	require.True(t, foundL1Token, "L1 token should be found in mixed query results")
}

func TestCoordinatedCreateNativeSparkTokenScenarios(t *testing.T) {
	fixedRandomKey := getRandomPrivateKey(t)

	testCases := []struct {
		name              string
		firstTokenParams  *sparkTokenCreationTestParams
		secondTokenParams *sparkTokenCreationTestParams
	}{
		{
			name: "create second token with same issuer key should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: fixedRandomKey,
				name:             TestTokenName,
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: fixedRandomKey,
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        TestTokenMaxSupply + 1000,
				expectedError:    true,
			},
		},
		{
			name: "create two tokens with same metadata but different random keys should succeed",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        TestTokenMaxSupply,
			},
		},
		{
			name: "create two tokens with different metadata and different random keys should succeed",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
			},
			secondTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "Different Name",
				ticker:           "DIFF",
				maxSupply:        TestTokenMaxSupply + 1000,
			},
		},
		{
			name: "create token with name longer than 20 characters should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "This Token Name Is Way Too Long For The System",
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with empty name should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             "",
				ticker:           TestTokenTicker,
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with empty ticker should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           "",
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
		{
			name: "create token with ticker longer than 5 characters should fail",
			firstTokenParams: &sparkTokenCreationTestParams{
				issuerPrivateKey: getRandomPrivateKey(t),
				name:             TestTokenName,
				ticker:           "TOOLONG",
				maxSupply:        TestTokenMaxSupply,
				expectedError:    true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			firstTokenConfig := sparktesting.TestWalletConfigWithIdentityKey(t, tc.firstTokenParams.issuerPrivateKey)

			// Create first token
			err := createNativeToken(t, *tc.firstTokenParams)
			if tc.firstTokenParams.expectedError {
				require.Error(t, err, "expected error but got none for first token creation")
				return
			}
			require.NoError(t, err, "unexpected error during first token creation")

			// Verify first token was created successfully (basic check)
			firstTokenIdentifier := verifyNativeToken(t, *tc.firstTokenParams)
			require.NotNil(t, firstTokenIdentifier, "first token should have been created successfully")

			// Create second token (if specified in the test params)
			if tc.secondTokenParams != nil {
				secondTokenConfig := sparktesting.TestWalletConfigWithIdentityKey(t, tc.secondTokenParams.issuerPrivateKey)

				err = testCoordinatedCreateNativeSparkTokenWithParams(t, secondTokenConfig, createNativeSparkTokenParams{
					IssuerPrivateKey: tc.secondTokenParams.issuerPrivateKey,
					Name:             tc.secondTokenParams.name,
					Ticker:           tc.secondTokenParams.ticker,
					MaxSupply:        tc.secondTokenParams.maxSupply,
				})
				if tc.secondTokenParams.expectedError {
					require.Error(t, err, "expected error but got none for second token creation")
					stat, ok := status.FromError(err)
					require.True(t, ok, "expected error to be a gRPC status error")
					require.Equal(t, codes.AlreadyExists, stat.Code(), "expected gRPC status code to be AlreadyExists when token already created for issuer")
				} else {
					require.NoError(t, err, "unexpected error during second token creation")

					// Verify second token was created successfully and test multi-token query
					secondTokenIdentifier := verifyNativeToken(t, *tc.secondTokenParams)
					require.NotNil(t, secondTokenIdentifier, "second token should have been created successfully")

					// Test querying both tokens by their identifiers in one RPC call
					verifyMultipleTokenIdentifiersQuery(t, firstTokenConfig, [][]byte{
						firstTokenIdentifier,
						secondTokenIdentifier,
					}, 2)
				}
			}
		})
	}
}

// Helper function to verify querying for multiple token identifiers in a single RPC call
func verifyMultipleTokenIdentifiersQuery(t *testing.T, config *wallet.TestWalletConfig, tokenIdentifiers [][]byte, expectedCount int) {
	// Query for multiple tokens using their identifiers in a single RPC call
	resp, err := wallet.QueryTokenMetadata(t.Context(), config, tokenIdentifiers, nil)
	require.NoError(t, err, "failed to query multiple tokens by their identifiers")
	require.Len(t, resp.TokenMetadata, expectedCount, "expected exactly %d token metadata entries when querying multiple tokens", expectedCount)

	// Verify that all requested token identifiers are present in the response
	responseIdentifiers := make(map[string]bool)
	for _, metadata := range resp.TokenMetadata {
		responseIdentifiers[string(metadata.TokenIdentifier)] = true
	}

	for i, tokenID := range tokenIdentifiers {
		require.Contains(t, responseIdentifiers, string(tokenID), "token identifier %d should be present in response", i)
	}
}

// Helper function for testing token mint/transfer transaction with various start/commit scenarios
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
		OutputAmounts:                  []uint64{uint64(TestTransferOutput1Amount)},
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

// Helper function for testing token mint transaction with various signing scenarios
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
		OutputAmounts:       []uint64{uint64(TestIssueOutput1Amount), uint64(TestIssueOutput2Amount)},
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
			explicitWalletPrivateKey: getRandomPrivateKey(t),
		},
		{
			name:                     "mint should succeed with native spark token with token identifier",
			createNativeSparkToken:   true,
			useTokenIdentifier:       true,
			explicitWalletPrivateKey: getRandomPrivateKey(t),
		},
		{
			name:                     "mint should fail with no associated token create",
			explicitWalletPrivateKey: getRandomPrivateKey(t),
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
			issuerStartPrivateKeys: []keys.Private{getRandomPrivateKey(t)},
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
			issuerCommitPrivateKeys: []keys.Private{getRandomPrivateKey(t)},
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

			config := sparktesting.TestWalletConfigWithIdentityKey(t, issuerPrivateKey)

			if tc.createNativeSparkToken {
				err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
					IssuerPrivateKey: issuerPrivateKey,
					Name:             TestTokenName,
					Ticker:           TestTokenTicker,
					MaxSupply:        TestTokenMaxSupply,
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
			explicitWalletPrivateKey: getRandomPrivateKey(t),
		},
		{
			name:                     "transfer should succeed with native spark token with token identifier",
			createNativeSparkToken:   true,
			useTokenIdentifier:       true,
			explicitWalletPrivateKey: getRandomPrivateKey(t),
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

			config := sparktesting.TestWalletConfigWithIdentityKey(t, issuerPrivateKey)

			if tc.createNativeSparkToken {
				err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
					IssuerPrivateKey: issuerPrivateKey,
					Name:             TestTokenName,
					Ticker:           TestTokenTicker,
					MaxSupply:        TestTokenMaxSupply,
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

// TestCoordinatedTransferTransactionWithSparkInvoices tests various start scenarios for token transfer transactions with associated spark invoices
func TestCoordinatedTransferTransactionWithSparkInvoices(t *testing.T) {
	// TODO: (CNT-493) Re-enable invoice functionality once spark address migration is complete
	skipIfGithubActions(t)
	testCases := []struct {
		name                                      string
		batchTransfer                             bool
		mismatchedIdentifier                      bool
		mismatchedOwner                           bool
		emptyInvoiceAmount                        bool
		invoiceAmountGreaterThanCreatedOutputs    bool
		invoiceAmountLessThanCreatedOutputs       bool
		expiredInvoice                            bool
		satsInvoice                               bool
		expiredAtSign                             bool
		transferFailsIfUnexpiredTransactionExists bool
		signedInvoice                             bool
		invalidSignature                          bool
		mismatchedSenderPublicKey                 bool
		emptySenderPublicKey                      bool
		mismatchedNetwork                         bool
	}{
		{
			name: "transfer should succeed with valid spark invoice",
		},
		{
			name:          "transfer should succeed with valid signed spark invoice",
			signedInvoice: true,
		},
		{
			name:             "transfer should fail with invalid spark invoice signature",
			signedInvoice:    true,
			invalidSignature: true,
		},
		{
			name:          "batch transfer should succeed with valid spark invoices",
			batchTransfer: true,
		},
		{
			name:               "transfer should succeed with empty invoice amount",
			emptyInvoiceAmount: true,
		},
		{
			name:               "batch transfer should succeed with empty invoice amount",
			batchTransfer:      true,
			emptyInvoiceAmount: true,
		},
		{
			name:            "transfer should fail with mismatched owner",
			mismatchedOwner: true,
		},
		{
			name:            "batch transfer should fail with mismatched owner",
			batchTransfer:   true,
			mismatchedOwner: true,
		},
		{
			name:                 "transfer should fail with mismatched identifier",
			mismatchedIdentifier: true,
		},
		{
			name:                 "batch transfer should fail with mismatched identifier",
			batchTransfer:        true,
			mismatchedIdentifier: true,
		},
		{
			name:                                   "transfer should fail with invoice amount greater than created outputs",
			invoiceAmountGreaterThanCreatedOutputs: true,
		},
		{
			name:                                "transfer should fail with invoice amount less than created outputs",
			invoiceAmountLessThanCreatedOutputs: true,
		},
		{
			name:           "transfer should fail with expired spark invoice",
			expiredInvoice: true,
		},
		{
			name:           "batch transfer should fail with expired spark invoice",
			batchTransfer:  true,
			expiredInvoice: true,
		},
		{
			name:        "transfer should fail with sats spark invoice",
			satsInvoice: true,
		},
		{
			name: "new transfers should fail if paying an invoice that is already attached to an unexpired transaction",
			transferFailsIfUnexpiredTransactionExists: true,
		},
		{
			name:          "batch transfer should fail if paying an invoice that is already attached to an unexpired transaction",
			batchTransfer: true,
			transferFailsIfUnexpiredTransactionExists: true,
		},
		{
			name:          "sign should fail when a spark invoice is expired",
			expiredAtSign: true,
		},
		{
			name:                      "transfer should fail when a spark invoice encodes a sender pub key that does not match the owner of the spent outputs on the token transaction",
			mismatchedSenderPublicKey: true,
		},
		{
			name:                 "transfer should succeed when no sender public key is encoded",
			emptySenderPublicKey: true,
		},
		{
			name:              "transfer should fail when a spark invoice encodes a mismatched network",
			mismatchedNetwork: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivateKey := getRandomPrivateKey(t)
			config := sparktesting.TestWalletConfigWithIdentityKey(t, issuerPrivateKey)

			tokenPrivKey := config.IdentityPrivateKey
			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
				IssuerPrivateKey: issuerPrivateKey,
				Name:             TestTokenName,
				Ticker:           TestTokenTicker,
				MaxSupply:        TestTokenMaxSupply,
			})
			require.NoError(t, err, "failed to create native spark token")

			issueTokenTransaction, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: tokenPrivKey.Public(),
				IsNativeSparkToken:  false,
				UseTokenIdentifier:  true,
				NumOutputs:          2,
				OutputAmounts:       []uint64{uint64(TestIssueOutput1Amount), uint64(TestIssueOutput2Amount)},
				MintToSelf:          true,
			})
			require.NoError(t, err, "failed to create test token issuance transaction")
			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			testCoordinatedTransferTransactionWithSparkInvoicesScenarios(
				t, config, finalIssueTokenTransaction,
				tokenPrivKey.Public(),
				tc.batchTransfer,
				tc.mismatchedIdentifier,
				tc.mismatchedOwner,
				tc.emptyInvoiceAmount,
				tc.invoiceAmountGreaterThanCreatedOutputs,
				tc.invoiceAmountLessThanCreatedOutputs,
				tc.expiredInvoice,
				tc.satsInvoice,
				tc.expiredAtSign,
				tc.transferFailsIfUnexpiredTransactionExists,
				tc.signedInvoice,
				tc.invalidSignature,
				tc.mismatchedSenderPublicKey,
				tc.emptySenderPublicKey,
				tc.mismatchedNetwork,
			)
		})
	}
}

func testCoordinatedTransferTransactionWithSparkInvoicesScenarios(t *testing.T, config *wallet.TestWalletConfig, finalIssueTokenTransaction *tokenpb.TokenTransaction, tokenIdentityPubKey keys.Public, batchTransfer bool, mismatchedIdentifier bool, mismatchedOwner bool, emptyInvoiceAmount bool, invoiceAmountGreaterThanCreatedOutputs bool, invoiceAmountLessThanCreatedOutputs bool, expiredInvoice bool, satsInvoice bool, expiredAtSign bool, transferFailsIfUnexpiredTransactionExists bool, signedInvoice bool, invalidSignature bool, mismatchedSenderPublicKey bool, emptySenderPublicKey bool, mismatchedNetwork bool) {
	finalMintTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issue token transaction")
	tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, tokenIdentityPubKey)
	require.NoError(t, err, "failed to get token identifier from metadata")
	receiver1, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")
	receiver1PubKey := receiver1.Public()
	receiver2, err := keys.GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")
	receiver2PubKey := receiver2.Public()

	expiryTime := timestamppb.New(time.Now().Add(time.Minute * 30))
	if expiredInvoice {
		expiryTime = timestamppb.New(time.Now().Add(-time.Minute * 30))
	} else if expiredAtSign {
		expiryTime = timestamppb.New(time.Now().Add(time.Second * 4))
	}

	var transferTransaction *tokenpb.TokenTransaction
	var nonBatchReceiver keys.Private
	if !batchTransfer {
		transferTransaction, nonBatchReceiver, err = createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey:            tokenIdentityPubKey,
			IsNativeSparkToken:             true,
			UseTokenIdentifier:             true,
			FinalIssueTokenTransactionHash: finalMintTransactionHash,
			NumOutputs:                     1,
			OutputAmounts:                  []uint64{uint64(TestTransferOutput1Amount)},
		})
		require.NoError(t, err, "failed to create transfer transaction")
	} else {
		transferTransaction = &tokenpb.TokenTransaction{
			Version: TokenTransactionVersion2,
			TokenInputs: &tokenpb.TokenTransaction_TransferInput{
				TransferInput: &tokenpb.TokenTransferInput{
					OutputsToSpend: []*tokenpb.TokenOutputToSpend{
						{
							PrevTokenTransactionHash: finalMintTransactionHash,
							PrevTokenTransactionVout: 0,
						},
						{
							PrevTokenTransactionHash: finalMintTransactionHash,
							PrevTokenTransactionVout: 1,
						},
					},
				},
			},
			TokenOutputs: []*tokenpb.TokenOutput{
				{
					OwnerPublicKey:  receiver1PubKey.Serialize(),
					TokenIdentifier: tokenIdentifier,
					TokenAmount:     int64ToUint128Bytes(0, TestIssueOutput1Amount),
				},
				{
					OwnerPublicKey:  receiver2PubKey.Serialize(),
					TokenIdentifier: tokenIdentifier,
					TokenAmount:     int64ToUint128Bytes(0, TestIssueOutput2Amount),
				},
			},
			Network:                         config.ProtoNetwork(),
			SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
			ClientCreatedTimestamp:          timestamppb.New(time.Now()),
		}
	}

	rng := rand.NewChaCha8([32]byte{})
	var invoiceAttachments []*tokenpb.InvoiceAttachment
	for _, output := range transferTransaction.TokenOutputs {
		receiverPublicKey, _ := keys.ParsePublicKey(output.GetOwnerPublicKey())
		tokenIdentifier := make([]byte, len(output.TokenIdentifier))
		copy(tokenIdentifier, output.TokenIdentifier)
		version := uint32(1)
		senderPublicKey := config.IdentityPrivateKey.Public()
		memo := "Test memo"
		network := config.Network
		satsPayment := false

		var amount *uint64
		if !emptyInvoiceAmount {
			amount = new(uint64)
			amountToEncode := binary.BigEndian.Uint64(output.TokenAmount[8:])
			if invoiceAmountGreaterThanCreatedOutputs {
				amountToEncode += 2
			} else if invoiceAmountLessThanCreatedOutputs {
				amountToEncode -= 2
			}
			*amount = amountToEncode
		}
		if mismatchedIdentifier {
			tokenIdentifier[0] ^= 0xFF
		}
		if mismatchedOwner {
			receiverPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
		}
		if satsInvoice {
			satsPayment = true
		}
		if mismatchedSenderPublicKey {
			senderPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
		}
		if emptySenderPublicKey {
			senderPublicKey = keys.Public{}
		}
		if mismatchedNetwork {
			if config.Network == common.Mainnet {
				network = common.Regtest
			} else {
				network = common.Mainnet
			}
		}

		createParams := createSparkInvoiceParams{
			Version:           version,
			ReceiverPublicKey: receiverPublicKey,
			SenderPublicKey:   senderPublicKey,
			Amount:            amount,
			ExpiryTime:        expiryTime,
			Memo:              &memo,
			TokenIdentifier:   tokenIdentifier,
			Network:           network,
			SatsPayment:       satsPayment,
		}

		// If signature testing is requested, set signer for helper to embed the signature
		if signedInvoice {
			if invalidSignature {
				s, err := keys.GeneratePrivateKey()
				require.NoError(t, err)
				createParams.SignerPrivKey = s
			} else {
				if batchTransfer {
					// Batch: use the corresponding receiver key by matching public keys
					if receiverPublicKey.Equals(receiver1PubKey) {
						createParams.SignerPrivKey = receiver1
					} else {
						createParams.SignerPrivKey = receiver2
					}
				} else {
					createParams.SignerPrivKey = nonBatchReceiver
				}
			}
		}

		sparkInvoice, err := createSparkInvoice(createParams)
		require.NoError(t, err, "failed to create spark invoice")
		attachment := &tokenpb.InvoiceAttachment{SparkInvoice: sparkInvoice}
		invoiceAttachments = append(invoiceAttachments, attachment)
	}
	transferTransaction.InvoiceAttachments = invoiceAttachments

	startResp, finalTxHash, err := wallet.StartTokenTransactionCoordinated(
		t.Context(),
		config,
		transferTransaction,
		[]keys.Private{config.IdentityPrivateKey, config.IdentityPrivateKey},
		wallet.DefaultValidityDurationSecs,
		nil,
	)

	if mismatchedIdentifier {
		require.Error(t, err, "expected error when mismatched identifier")
		return
	} else if mismatchedOwner {
		require.Error(t, err, "expected error when mismatched owner")
		return
	} else if expiredInvoice {
		require.Error(t, err, "expected error when expired spark invoice")
		return
	} else if satsInvoice {
		require.Error(t, err, "expected error when sats spark invoice")
		return
	} else if invoiceAmountGreaterThanCreatedOutputs {
		require.Error(t, err, "expected error when invoice amount greater than created outputs")
		return
	} else if invoiceAmountLessThanCreatedOutputs {
		require.Error(t, err, "expected error when invoice amount less than created outputs")
		return
	} else if invalidSignature {
		require.Error(t, err, "expected error when invalid spark invoice signature")
		return
	} else if mismatchedSenderPublicKey {
		require.Error(t, err, "expected error when mismatched sender public key")
		return
	} else if mismatchedNetwork {
		require.Error(t, err, "expected error when mismatched network")
		return
	} else {
		require.NoError(t, err, "expected no error when valid spark invoice")
	}

	if transferFailsIfUnexpiredTransactionExists {
		issueTokenTransaction, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: tokenIdentityPubKey,
			IsNativeSparkToken:  false,
			UseTokenIdentifier:  true,
			NumOutputs:          2,
			OutputAmounts:       []uint64{uint64(TestIssueOutput1Amount), uint64(TestIssueOutput2Amount)},
			MintToSelf:          true,
		})
		require.NoError(t, err, "failed to create test token issuance transaction")
		finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
			t.Context(), config, issueTokenTransaction,
			[]keys.Private{config.IdentityPrivateKey},
		)
		require.NoError(t, err, "failed to broadcast issuance token transaction")
		finalMintTransactionHash, err = utils.HashTokenTransaction(finalIssueTokenTransaction, false)
		require.NoError(t, err, "failed to hash final issue token transaction")

		var shouldFailTransfer *tokenpb.TokenTransaction
		if !batchTransfer {
			shouldFailTransfer, _, err = createTestTokenTransferTransactionTokenPb(t, config, finalMintTransactionHash, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create transfer transaction")
		} else {
			shouldFailTransfer = &tokenpb.TokenTransaction{
				Version: TokenTransactionVersion2,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: finalMintTransactionHash,
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: finalMintTransactionHash,
								PrevTokenTransactionVout: 1,
							},
						},
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						OwnerPublicKey:  receiver1PubKey.Serialize(),
						TokenIdentifier: tokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, TestIssueOutput1Amount),
					},
					{
						OwnerPublicKey:  receiver2PubKey.Serialize(),
						TokenIdentifier: tokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, TestIssueOutput2Amount),
					},
				},
				Network:                         config.ProtoNetwork(),
				SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
				ClientCreatedTimestamp:          timestamppb.New(time.Now()),
			}
		}
		shouldFailTransfer.InvoiceAttachments = invoiceAttachments

		_, _, err = wallet.StartTokenTransactionCoordinated(
			t.Context(),
			config,
			shouldFailTransfer,
			[]keys.Private{config.IdentityPrivateKey, config.IdentityPrivateKey},
			wallet.DefaultValidityDurationSecs,
			nil,
		)
		require.Error(t, err, "expected error when transfer fails if unexpired transaction exists")
		return
	}
	startResponseTransactionResult := &TransactionResult{
		config:     config,
		resp:       startResp,
		txFullHash: finalTxHash,
	}

	if expiredAtSign {
		time.Sleep(time.Second * 6)
	}
	_, err = signAndCommitTransaction(t, startResponseTransactionResult, []keys.Private{config.IdentityPrivateKey, config.IdentityPrivateKey})
	if expiredAtSign {
		require.Error(t, err, "expected error when expired at sign")
		return
	}
	require.NoError(t, err, "failed to sign and commit transaction")

	queryTokenTransactionParms := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  nil,
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: [][]byte{finalTxHash},
		Offset:            0,
		Limit:             1,
	}
	tokenTransactionResponse, err := wallet.QueryTokenTransactionsV2(
		t.Context(),
		config,
		queryTokenTransactionParms,
	)
	require.NoError(t, err, "failed to query token transactions")
	require.Len(t, tokenTransactionResponse.TokenTransactionsWithStatus, 1, "expected 1 token transaction")
	// match the length of the outputs since we create one spark invoice per output in batch testing
	expectedLen := len(transferTransaction.TokenOutputs)
	require.Len(t, tokenTransactionResponse.TokenTransactionsWithStatus[0].TokenTransaction.GetInvoiceAttachments(), expectedLen, "expected same number of outputs")

	invoicesToQuery := make([]string, 0, len(invoiceAttachments))
	for _, invoiceAttachment := range invoiceAttachments {
		invoicesToQuery = append(invoicesToQuery, invoiceAttachment.GetSparkInvoice())
	}
	invoiceResponse, err := wallet.QuerySparkInvoicesByRawString(
		t.Context(),
		config,
		invoicesToQuery,
	)
	require.NoError(t, err, "failed to query spark invoices")
	require.Len(t, invoiceResponse.InvoiceStatuses, len(invoicesToQuery))
	for i, invoiceResponse := range invoiceResponse.InvoiceStatuses {
		require.Equal(t, invoiceResponse.Invoice, invoicesToQuery[i])
		require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, invoiceResponse.Status)
		require.Equal(t, &sparkpb.InvoiceResponse_TokenTransfer{
			TokenTransfer: &sparkpb.TokenTransfer{
				FinalTokenTransactionHash: finalTxHash[:],
			},
		}, invoiceResponse.TransferType)
	}
}

type createSparkInvoiceParams struct {
	Version           uint32
	ReceiverPublicKey keys.Public
	SenderPublicKey   keys.Public
	Amount            *uint64
	ExpiryTime        *timestamppb.Timestamp
	Memo              *string
	TokenIdentifier   []byte
	Network           common.Network
	SatsPayment       bool
	// Optional: include a signature by the receiver over the invoice fields
	SignerPrivKey keys.Private
}

func createSparkInvoice(params createSparkInvoiceParams) (string, error) {
	version := params.Version
	receiverPublicKey := params.ReceiverPublicKey
	senderPublicKeyPtr := params.SenderPublicKey
	amount := params.Amount
	expiryTime := params.ExpiryTime
	memo := params.Memo
	tokenIdentifier := params.TokenIdentifier
	network := params.Network
	satsPayment := params.SatsPayment

	var senderPublicKey []byte
	if senderPublicKeyPtr != (keys.Public{}) {
		senderPublicKey = senderPublicKeyPtr.Serialize()
	}

	uuid := uuid.New()
	sparkInvoiceFields := &sparkpb.SparkInvoiceFields{
		Version:         version,
		Id:              uuid[:],
		ExpiryTime:      expiryTime,
		Memo:            memo,
		SenderPublicKey: senderPublicKey,
	}
	if satsPayment {
		sparkInvoiceFields.PaymentType = &sparkpb.SparkInvoiceFields_SatsPayment{
			SatsPayment: &sparkpb.SatsPayment{
				Amount: amount,
			},
		}
	} else {
		var amountBytes []byte
		if amount != nil {
			amountBytes = int64ToUint128Bytes(0, *amount)
		}
		sparkInvoiceFields.PaymentType = &sparkpb.SparkInvoiceFields_TokensPayment{
			TokensPayment: &sparkpb.TokensPayment{
				TokenIdentifier: tokenIdentifier,
				Amount:          amountBytes,
			},
		}
	}
	// If a signer key is provided, compute a signature and use the WithSignature helper
	if params.SignerPrivKey != (keys.Private{}) {
		hash, err := common.HashSparkInvoiceFields(sparkInvoiceFields, network, receiverPublicKey)
		if err != nil {
			return "", err
		}
		sig, err := schnorr.Sign(params.SignerPrivKey.ToBTCEC(), hash)
		if err != nil {
			return "", err
		}
		return common.EncodeSparkAddressWithSignature(receiverPublicKey.Serialize(), network, sparkInvoiceFields, sig.Serialize())
	}

	sparkAddress, err := common.EncodeSparkAddress(receiverPublicKey.Serialize(), network, sparkInvoiceFields)
	if err != nil {
		return "", err
	}
	return sparkAddress, nil
}

type CoordinatorScenario struct {
	name            string
	sameCoordinator bool
}

type TimestampScenario struct {
	name          string
	timestampMode TimestampScenarioMode
}

type PreemptionTestCase struct {
	name                  string
	sameCoordinator       bool
	timestampMode         TimestampScenarioMode
	secondRequestScenario SecondRequestScenarioMode
}

type TransactionResult struct {
	config        *wallet.TestWalletConfig
	resp          *tokenpb.StartTransactionResponse
	txFullHash    []byte
	txPartialHash []byte
}

type TimestampScenarioMode int

const (
	// Set both timestamps to the same time to trigger hash-based pre-emption
	TimestampScenarioEqual TimestampScenarioMode = iota
	TimestampScenarioFirstEarlier
	TimestampScenarioSecondEarlier
	TimestampScenarioExpired
)

type SecondRequestScenarioMode int

const (
	SecondRequestScenarioAfterStart SecondRequestScenarioMode = iota
	SecondRequestScenarioAfterSignTokenTransactionFromCoordination
)

type SecondRequestScenario struct {
	name                  string
	secondRequestScenario SecondRequestScenarioMode
}

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
		// TODO CNT-374: This seems to be flakey in CI
		// {
		// 	name:          "hash-based pre-emption",
		// 	timestampMode: TimestampScenarioEqual,
		// },
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
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			tokenPrivKey := config.IdentityPrivateKey
			tokenIdentityPubKey := tokenPrivKey.Public()

			config1 := config
			var config2 *wallet.TestWalletConfig
			if tc.sameCoordinator {
				config2 = config
			} else {
				config2 = sparktesting.TestWalletConfigWithParams(t,
					sparktesting.TestWalletConfigParams{
						IdentityPrivateKey: staticLocalIssuerKey.IdentityPrivateKey(),
						CoordinatorIndex:   1,
					},
				)
			}

			// For transfers, we need to create a mint first to have outputs to spend
			mintTransaction, userOutput1PrivKey, userOutput2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config1, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create mint transaction for transfer test")

			finalMintTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config1, mintTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast mint transaction for transfer test")

			finalMintTransactionHash, err := utils.HashTokenTransaction(finalMintTransaction, false)
			require.NoError(t, err, "failed to hash mint transaction")

			// Create first transfer transaction
			transaction1, _, err := createTestTokenTransferTransactionTokenPb(t, config1, finalMintTransactionHash, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create first transfer transaction")

			// Create second transfer transaction for a different random recipient
			transaction2, _, err := createTestTokenTransferTransactionTokenPb(t, config2, finalMintTransactionHash, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create second transfer transaction")

			setTransactionTimestamps(transaction1, transaction2, tc.timestampMode)

			txPartialHash1, err := utils.HashTokenTransaction(transaction1, true)
			require.NoError(t, err, "failed to hash first transfer transaction")
			txPartialHash2, err := utils.HashTokenTransaction(transaction2, true)
			require.NoError(t, err, "failed to hash second transfer transaction")

			t1ExpiryDuration := uint64(180)
			if tc.timestampMode == TimestampScenarioExpired {
				t1ExpiryDuration = 1
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

			resp2, resp2Hash, err := wallet.StartTokenTransactionCoordinated(t.Context(), config2, transaction2, []keys.Private{userOutput1PrivKey, userOutput2PrivKey}, 180, nil)
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

// setTransactionTimestamps sets the client timestamps on both transactions based on the test scenario
func setTransactionTimestamps(transaction1, transaction2 *tokenpb.TokenTransaction, timestampMode TimestampScenarioMode) {
	now := time.Now()
	switch timestampMode {
	case TimestampScenarioEqual:
		transaction1.ClientCreatedTimestamp = timestamppb.New(now)
		transaction2.ClientCreatedTimestamp = timestamppb.New(now)
	case TimestampScenarioFirstEarlier, TimestampScenarioExpired:
		transaction1.ClientCreatedTimestamp = timestamppb.New(now.Add(-time.Second))
		transaction2.ClientCreatedTimestamp = timestamppb.New(now)
	case TimestampScenarioSecondEarlier:
		transaction1.ClientCreatedTimestamp = timestamppb.New(now)
		transaction2.ClientCreatedTimestamp = timestamppb.New(now.Add(-time.Second))
	default:
		panic(fmt.Sprintf("unknown timestamp scenario mode: %d", timestampMode))
	}
}

// determineWinningAndLosingTransactions determines which transaction should win based on the test scenario
// and returns the appropriate TransactionResult structs for the winning and losing transactions
func determineWinningAndLosingTransactions(
	tc PreemptionTestCase,
	transactionResult1, transactionResult2 *TransactionResult,
) (*TransactionResult, *TransactionResult) {
	var firstShouldWin bool

	switch tc.timestampMode {
	case TimestampScenarioEqual:
		firstShouldWin = bytes.Compare(transactionResult1.txPartialHash, transactionResult2.txPartialHash) < 0
	case TimestampScenarioFirstEarlier:
		firstShouldWin = true
	case TimestampScenarioSecondEarlier, TimestampScenarioExpired:
		firstShouldWin = false
	default:
		panic(fmt.Sprintf("unknown timestamp scenario mode: %d", tc.timestampMode))
	}

	var winningResult, losingResult *TransactionResult
	if firstShouldWin {
		winningResult = transactionResult1
		losingResult = nil
	} else {
		winningResult = transactionResult2
		losingResult = transactionResult1
	}

	return winningResult, losingResult
}

func signAndCommitTransaction(t *testing.T, transactionResult *TransactionResult, ownerPrivateKeys []keys.Private) (*tokenpb.CommitTransactionResponse, error) {
	operatorSignatures, err := wallet.CreateOperatorSpecificSignatures(
		transactionResult.config,
		ownerPrivateKeys,
		transactionResult.txFullHash,
	)
	require.NoError(t, err, "failed to create operator-specific signatures for winning transaction")

	commitReq := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          transactionResult.resp.FinalTokenTransaction,
		FinalTokenTransactionHash:      transactionResult.txFullHash,
		InputTtxoSignaturesPerOperator: operatorSignatures,
		OwnerIdentityPublicKey:         transactionResult.config.IdentityPublicKey().Serialize(),
	}

	return wallet.CommitTransactionCoordinated(t.Context(), transactionResult.config, commitReq)
}

// TestQueryTokenOutputsWithExpiredTransaction verifies that when a transfer
// transaction expires without being finalized, the spent outputs are returned again by
// QueryTokenOutputsV2.
func TestQueryTokenOutputsWithStartTransaction(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
			config.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

			issuerPrivKey := config.IdentityPrivateKey
			mintTx, owner1PrivKey, owner2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
			require.NoError(t, err, "failed to create mint transaction")

			finalTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, mintTx, []keys.Private{issuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast mint transaction")

			mintTxHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
			require.NoError(t, err, "failed to hash mint transaction")

			transferTx, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey:            issuerPrivKey.Public(),
				FinalIssueTokenTransactionHash: mintTxHash,
				NumOutputs:                     1,
				OutputAmounts:                  []uint64{uint64(TestTransferOutput1Amount)},
			})
			require.NoError(t, err, "failed to create transfer transaction")

			_, _, err = wallet.StartTokenTransactionCoordinated(t.Context(), config, transferTx, []keys.Private{owner1PrivKey, owner2PrivKey}, 1, nil)
			require.NoError(t, err, "failed to start transfer transaction")

			outputsResp, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				config,
				[]keys.Public{owner1PrivKey.Public()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query token outputs")

			require.Len(t, outputsResp.OutputsWithPreviousTransactionData, 1, "expected the spent output to be returned after transaction expiry")
			require.Equal(t, mintTxHash, outputsResp.OutputsWithPreviousTransactionData[0].PreviousTransactionHash, "expected the same previous transaction hash")
		})
	}
}

func TestQueryTokenOutputsWithRevealedRevocationSecrets(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())

	issuerPrivKey := config.IdentityPrivateKey
	mintTx, owner1PrivKey, owner2PrivKey, err := createTestTokenMintTransactionTokenPb(t, config, issuerPrivKey.Public())
	require.NoError(t, err, "failed to create mint transaction")

	finalTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
		t.Context(), config, mintTx, []keys.Private{issuerPrivKey},
	)
	require.NoError(t, err, "failed to broadcast mint transaction")

	mintTxHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	require.NoError(t, err, "failed to hash mint transaction")

	transferTx, _, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey:            issuerPrivKey.Public(),
		FinalIssueTokenTransactionHash: mintTxHash,
		NumOutputs:                     1,
		OutputAmounts:                  []uint64{uint64(TestTransferOutput1Amount)},
	})
	require.NoError(t, err, "failed to create transfer transaction")

	// Start the coordinated transaction
	startResp, finalTxHash, err := wallet.StartTokenTransactionCoordinated(t.Context(), config, transferTx, []keys.Private{owner1PrivKey, owner2PrivKey}, 1, nil)
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

	// Verify that the transaction is in the correct state after signing with all operators
	// but before revocation secret exchange
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

	// After the revocation secret exchange, the outputs should no longer be queryable from the exchanging operator
	queryAndVerifyNoTokenOutputs(t, []string{exchangingOperator.Identifier}, owner1PrivKey)

	// Query from all unexchanged operators to verify they still see the outputs
	var unexchangedOperatorIdentifiers []string
	for identifier := range config.SigningOperators {
		if identifier != exchangingOperator.Identifier {
			unexchangedOperatorIdentifiers = append(unexchangedOperatorIdentifiers, identifier)
		}
	}
	queryAndVerifyTokenOutputs(t, unexchangedOperatorIdentifiers, finalTokenTransaction, owner1PrivKey)
}

// TestPartialTransactionValidationErrors tests that partial transactions with SO-only fields return errors
func TestPartialTransactionValidationErrors(t *testing.T) {
	config := sparktesting.TestWalletConfigWithIdentityKey(t, staticLocalIssuerKey.IdentityPrivateKey())
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
				t.Context(), config, tokenTransaction, ownerPrivateKeys, TestValidityDurationSecs, nil,
			)

			require.ErrorContains(t, err, tc.expectedErrorSubstr, "error message should contain expected substring")
		})
	}
}

// TestCoordinatedTokenMintV3 tests token minting using V3 transactions
func TestCoordinatedTokenMintV3(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivKey := getRandomPrivateKey(t)
			config := sparktesting.TestWalletConfigWithIdentityKey(t, issuerPrivKey)

			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
				IssuerPrivateKey: issuerPrivKey,
				Name:             TestTokenName,
				Ticker:           TestTokenTicker,
				MaxSupply:        TestTokenMaxSupply,
			})
			require.NoError(t, err, "failed to create native spark token")

			// Create a V3 mint transaction
			issueTokenTransaction, userPrivKeys, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: issuerPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          2,
				OutputAmounts:       []uint64{uint64(TestIssueOutput1Amount), uint64(TestIssueOutput2Amount)},
				Version:             TokenTransactionVersion3,
			})
			require.NoError(t, err, "failed to create test token issuance transaction")
			require.Len(t, userPrivKeys, 2)
			userOutput1PrivKey := userPrivKeys[0]
			userOutput2PrivKey := userPrivKeys[1]

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{issuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 issuance token transaction")
			require.Len(t, finalIssueTokenTransaction.TokenOutputs, 2, "expected 2 created outputs in V3 mint transaction")
			require.Equal(t, TokenTransactionVersion3, int(finalIssueTokenTransaction.Version), "final transaction should be V3")

			userOneConfig := sparktesting.TestWalletConfigWithIdentityKey(t, userOutput1PrivKey)
			userTwoConfig := sparktesting.TestWalletConfigWithIdentityKey(t, userOutput2PrivKey)

			userOneBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userOneConfig,
				[]keys.Public{userOneConfig.IdentityPublicKey()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user one token outputs")

			userTwoBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userTwoConfig,
				[]keys.Public{userTwoConfig.IdentityPublicKey()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user two token outputs")

			require.Len(t, userOneBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user one")
			userOneAmount := bytesToBigInt(userOneBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(TestIssueOutput1Amount), userOneAmount,
				"user one should have correct token amount")

			require.Len(t, userTwoBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user two")
			userTwoAmount := bytesToBigInt(userTwoBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(TestIssueOutput2Amount), userTwoAmount,
				"user two should have correct token amount")
		})
	}
}

// TestCoordinatedTokenTransferV3 tests token transfers using V3 transactions
func TestCoordinatedTokenTransferV3(t *testing.T) {
	for _, tc := range signatureTypeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivKey := getRandomPrivateKey(t)
			config := sparktesting.TestWalletConfigWithIdentityKey(t, issuerPrivKey)

			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, createNativeSparkTokenParams{
				IssuerPrivateKey: issuerPrivKey,
				Name:             TestTokenName,
				Ticker:           TestTokenTicker,
				MaxSupply:        TestTokenMaxSupply,
			})
			require.NoError(t, err, "failed to create native spark token")

			issueTokenTransaction, userPrivKeys, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: issuerPrivKey.Public(),
				IsNativeSparkToken:  true,
				UseTokenIdentifier:  true,
				NumOutputs:          2,
				OutputAmounts:       []uint64{uint64(TestIssueOutput1Amount), uint64(TestIssueOutput2Amount)},
				Version:             TokenTransactionVersion3,
			})
			require.NoError(t, err, "failed to create test token issuance transaction")
			require.Len(t, userPrivKeys, 2)
			userOutput1PrivKey := userPrivKeys[0]
			userOutput2PrivKey := userPrivKeys[1]

			finalIssueTokenTransaction, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{issuerPrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 issuance token transaction")

			finalIssueTokenTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
			require.NoError(t, err, "failed to hash final issuance token transaction")

			transferTokenTransaction, userOutput3PrivKey, err := createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey:            issuerPrivKey.Public(),
				UseTokenIdentifier:             true,
				FinalIssueTokenTransactionHash: finalIssueTokenTransactionHash,
				Version:                        TokenTransactionVersion3,
			})
			require.NoError(t, err, "failed to create test token transfer transaction")

			// Add a valid spark invoice attachment for V3 testing. We use one invoice to
			// match the single created output in this transfer transaction.
			// TODO: Uncomment this when we have re-enabled spark invoices
			/*
				{
					output := transferTokenTransaction.TokenOutputs[0]
					receiverPubKey, err := keys.ParsePublicKey(output.GetOwnerPublicKey())
					require.NoError(t, err, "failed to parse receiver public key")
					createParams := createSparkInvoiceParams{
						Version:           1,
						ReceiverPublicKey: receiverPubKey,
						Amount:            nil, // nil amount allowed; validated against created outputs
						ExpiryTime:        timestamppb.New(time.Now().Add(10 * time.Minute)),
						Memo:              nil,
						TokenIdentifier:   output.GetTokenIdentifier(),
						Network:           config.Network,
						SatsPayment:       false,
					}
					inv, err := createSparkInvoice(createParams)
					require.NoError(t, err, "failed to create spark invoice")
					transferTokenTransaction.InvoiceAttachments = []*tokenpb.InvoiceAttachment{{SparkInvoice: inv}}
				}
			*/

			// Verify V3 version is set
			require.Equal(t, TokenTransactionVersion3, int(transferTokenTransaction.Version), "expected V3 version")

			// Verify invoice attachments are sorted
			// TODO: Uncomment this when we have re-enabled spark invoices
			/*
				invoices := transferTokenTransaction.InvoiceAttachments
				require.NotEmpty(t, invoices, "expected invoice attachments")
				for i := 1; i < len(invoices); i++ {
					require.Negative(t, strings.Compare(invoices[i-1].GetSparkInvoice(), invoices[i].GetSparkInvoice()),
						"invoice attachments must be in ascending order for V3")
				}
			*/

			transferTokenTransactionResponse, err := wallet.BroadcastCoordinatedTokenTransfer(
				t.Context(), config, transferTokenTransaction,
				[]keys.Private{userOutput1PrivKey, userOutput2PrivKey},
			)
			require.NoError(t, err, "failed to broadcast V3 transfer token transaction")

			// Verify that the transaction was processed with V3 version
			require.Equal(t, TokenTransactionVersion3, int(transferTokenTransactionResponse.Version), "final transfer transaction should be V3")

			require.Len(t, transferTokenTransactionResponse.TokenOutputs, 1, "expected 1 created output in V3 transfer transaction")

			userThreeConfig := sparktesting.TestWalletConfigWithIdentityKey(t, userOutput3PrivKey)
			userThreeBalance, err := wallet.QueryTokenOutputsV2(
				t.Context(),
				userThreeConfig,
				[]keys.Public{userThreeConfig.IdentityPublicKey()},
				[]keys.Public{issuerPrivKey.Public()},
			)
			require.NoError(t, err, "failed to query user three token outputs")

			require.Len(t, userThreeBalance.OutputsWithPreviousTransactionData, 1, "expected one output for user three")
			userThreeAmount := bytesToBigInt(userThreeBalance.OutputsWithPreviousTransactionData[0].Output.TokenAmount)
			require.Equal(t, uint64ToBigInt(TestTransferOutput1Amount), userThreeAmount,
				"user three should have correct token amount from V3 transfer")
		})
	}
}
