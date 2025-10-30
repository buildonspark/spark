package ent_test

import (
	"math/big"
	"testing"

	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
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

			f := entfixtures.New(t, ctx, entTx)

			tokenCreate := f.CreateTokenCreate(st.NetworkMainnet, nil, nil)

			inputAmount := big.NewInt(1000)
			outputAmount := big.NewInt(500)

			input := f.CreateStandaloneOutput(tokenCreate, inputAmount, st.TokenOutputStatusCreatedFinalized)

			tokenTx, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_2")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_2")).
				SetStatus(st.TokenTransactionStatusStarted).
				AddSpentOutput(input).
				Save(ctx)
			require.NoError(t, err)

			_ = f.CreateOutputForTransaction(tokenCreate, outputAmount, tokenTx, 0)

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

			f := entfixtures.New(t, ctx, entTx)

			tokenCreate := f.CreateTokenCreate(st.NetworkMainnet, nil, nil)

			amount := big.NewInt(1000)
			input := f.CreateStandaloneOutput(tokenCreate, amount, st.TokenOutputStatusCreatedFinalized)

			tx1, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_3")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_3")).
				SetStatus(st.TokenTransactionStatusStarted).
				AddSpentOutput(input).
				Save(ctx)
			require.NoError(t, err)

			_ = f.CreateOutputForTransaction(tokenCreate, amount, tx1, 0)

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

			f := entfixtures.New(t, ctx, entTx)

			tokenCreate := f.CreateTokenCreate(st.NetworkMainnet, nil, nil)

			amount := big.NewInt(1000)
			input1 := f.CreateStandaloneOutput(tokenCreate, amount, st.TokenOutputStatusCreatedFinalized)
			input2 := f.CreateStandaloneOutput(tokenCreate, amount, st.TokenOutputStatusCreatedFinalized)

			tx1, err := entTx.TokenTransaction.Create().
				SetPartialTokenTransactionHash([]byte("partial_hash_7")).
				SetFinalizedTokenTransactionHash([]byte("finalized_hash_7")).
				SetStatus(st.TokenTransactionStatusStarted).
				AddSpentOutput(input1).
				Save(ctx)
			require.NoError(t, err)

			_ = f.CreateOutputForTransaction(tokenCreate, amount, tx1, 0)

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

			_ = f.CreateOutputForTransaction(tokenCreate, big.NewInt(500), tx2, 0)

			err = tx2.Update().
				SetStatus(tc.status).
				Exec(ctx)
			require.Error(t, err, "moving to %s with unbalanced inputs/outputs should fail", tc.name)
		})
	}
}
