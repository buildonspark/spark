package ent_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

// validatedStatuses are the transaction statuses that require balance validation
var validatedStatuses = []struct {
	name   string
	status st.TokenTransactionStatus
}{
	{
		name:   "REVEALED",
		status: st.TokenTransactionStatusRevealed,
	},
	{
		name:   "FINALIZED",
		status: st.TokenTransactionStatusFinalized,
	},
}

func TestUnbalancedTransferFails(t *testing.T) {
	t.Parallel()

	for _, tc := range validatedStatuses {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.NewTestSQLiteContext(t)
			entTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			tokenCreate := createTestTokenCreate(t, ctx)

			inputAmount := big.NewInt(1000)
			outputAmount := big.NewInt(500)

			input := createTestOutput(t, ctx, tokenCreate, inputAmount, st.TokenOutputStatusCreatedFinalized)

			tokenTx, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_2")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_2")).
				SetStatus(st.TokenTransactionStatusStarted).
				AddSpentOutput(input).
				Save(ctx)
			require.NoError(t, err)

			_ = createTestOutputForTransaction(t, ctx, tokenCreate, outputAmount, tokenTx, 0)

			err = tokenTx.Update().
				SetStatus(tc.status).
				Exec(ctx)
			require.Error(t, err, "unbalanced transaction should not be allowed to move to %s", tc.name)
			require.Contains(t, err.Error(), "transaction balance validation failed")
		})
	}
}

func TestOutputReassignmentFromRevealedFails(t *testing.T) {
	t.Parallel()

	for _, tc := range validatedStatuses {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.NewTestSQLiteContext(t)
			entTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			tokenCreate := createTestTokenCreate(t, ctx)

			amount := big.NewInt(1000)
			input := createTestOutput(t, ctx, tokenCreate, amount, st.TokenOutputStatusCreatedFinalized)

			tx1, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_3")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_3")).
				SetStatus(st.TokenTransactionStatusStarted).
				AddSpentOutput(input).
				Save(ctx)
			require.NoError(t, err)

			_ = createTestOutputForTransaction(t, ctx, tokenCreate, amount, tx1, 0)

			err = tx1.Update().
				SetStatus(tc.status).
				Exec(ctx)
			require.NoError(t, err)

			tx2, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_4")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_4")).
				SetStatus(st.TokenTransactionStatusStarted).
				Save(ctx)
			require.NoError(t, err)

			// Try to reassign the input from tx1 to tx2, which should fail because tx1 is in a validated state and would become unbalanced
			err = input.Update().
				SetOutputSpentTokenTransaction(tx2).
				Exec(ctx)
			require.Error(t, err, "reassigning input from %s transaction should fail if it breaks balance", tc.name)
			require.Contains(t, err.Error(), "output reassignment would violate balance constraint")
		})
	}
}

func TestOutputReassignmentValidatesNewTransaction(t *testing.T) {
	t.Parallel()

	for _, tc := range validatedStatuses {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.NewTestSQLiteContext(t)
			entTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			tokenCreate := createTestTokenCreate(t, ctx)

			amount := big.NewInt(1000)
			input1 := createTestOutput(t, ctx, tokenCreate, amount, st.TokenOutputStatusCreatedFinalized)
			input2 := createTestOutput(t, ctx, tokenCreate, amount, st.TokenOutputStatusCreatedFinalized)

			tx1, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_7")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_7")).
				SetStatus(st.TokenTransactionStatusStarted).
				AddSpentOutput(input1).
				Save(ctx)
			require.NoError(t, err)

			_ = createTestOutputForTransaction(t, ctx, tokenCreate, amount, tx1, 0)

			err = tx1.Update().
				SetStatus(tc.status).
				Exec(ctx)
			require.NoError(t, err)

			tx2, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_8")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_8")).
				SetStatus(st.TokenTransactionStatusStarted).
				AddSpentOutput(input2).
				Save(ctx)
			require.NoError(t, err)

			_ = createTestOutputForTransaction(t, ctx, tokenCreate, big.NewInt(500), tx2, 0)

			err = tx2.Update().
				SetStatus(tc.status).
				Exec(ctx)
			require.Error(t, err, "moving to %s with unbalanced inputs/outputs should fail", tc.name)
		})
	}
}

func createTestTokenCreate(t *testing.T, ctx context.Context) *ent.TokenCreate {
	entTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	issuerKey := keys.GeneratePrivateKey()

	creationEntityKey := keys.GeneratePrivateKey()

	tokenCreate, err := entTx.TokenCreate.Create().
		SetIssuerPublicKey(issuerKey.Public()).
		SetTokenName("Test Token").
		SetTokenTicker("TST").
		SetDecimals(8).
		SetMaxSupply(big.NewInt(1000000).Bytes()).
		SetIsFreezable(false).
		SetNetwork(st.NetworkMainnet).
		SetTokenIdentifier([]byte("test_token_identifier_" + uuid.New().String())).
		SetCreationEntityPublicKey(creationEntityKey.Public()).
		Save(ctx)
	require.NoError(t, err)
	return tokenCreate
}

func createTestOutput(t *testing.T, ctx context.Context, tokenCreate *ent.TokenCreate, amount *big.Int, status st.TokenOutputStatus) *ent.TokenOutput {
	entTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerKey := keys.GeneratePrivateKey()

	keyshareKey := keys.GeneratePrivateKey()

	operatorKey := keys.GeneratePrivateKey()

	revocationKeyshare, err := entTx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keys.GeneratePrivateKey()).
		SetPublicShares(map[string]keys.Public{"operator1": operatorKey.Public()}).
		SetPublicKey(keyshareKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	output, err := entTx.TokenOutput.Create().
		SetStatus(status).
		SetOwnerPublicKey(ownerKey.Public()).
		SetWithdrawBondSats(1000).
		SetWithdrawRelativeBlockLocktime(144).
		SetWithdrawRevocationCommitment([]byte("commitment")).
		SetTokenAmount(amount.Bytes()).
		SetCreatedTransactionOutputVout(0).
		SetTokenIdentifier([]byte("token_id")).
		SetTokenCreate(tokenCreate).
		SetRevocationKeyshare(revocationKeyshare).
		Save(ctx)
	require.NoError(t, err)

	return output
}

func createTestOutputForTransaction(t *testing.T, ctx context.Context, tokenCreate *ent.TokenCreate, amount *big.Int, tokenTx *ent.TokenTransaction, vout int32) *ent.TokenOutput {
	entTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerKey := keys.GeneratePrivateKey()

	keyshareKey := keys.GeneratePrivateKey()

	operatorKey := keys.GeneratePrivateKey()

	revocationKeyshare, err := entTx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keys.GeneratePrivateKey()).
		SetPublicShares(map[string]keys.Public{"operator1": operatorKey.Public()}).
		SetPublicKey(keyshareKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	output, err := entTx.TokenOutput.Create().
		SetStatus(st.TokenOutputStatusCreatedStarted).
		SetOwnerPublicKey(ownerKey.Public()).
		SetWithdrawBondSats(1000).
		SetWithdrawRelativeBlockLocktime(144).
		SetWithdrawRevocationCommitment([]byte("commitment")).
		SetTokenAmount(amount.Bytes()).
		SetCreatedTransactionOutputVout(vout).
		SetTokenIdentifier([]byte("token_id")).
		SetTokenCreate(tokenCreate).
		SetRevocationKeyshare(revocationKeyshare).
		SetOutputCreatedTokenTransaction(tokenTx).
		Save(ctx)
	require.NoError(t, err)

	return output
}
