package task

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestBackfillSpentTokenTransactionHistory(t *testing.T) {
	seededRand := rand.NewChaCha8([32]byte{})
	randomBytes := func(n int) []byte {
		b := make([]byte, n)
		_, _ = seededRand.Read(b)
		return b
	}
	ctx, _ := db.NewTestSQLiteContext(t)

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	config := sparktesting.TestConfig(t)
	config.Token.EnableBackfillSpentTokenTransactionHistoryTask = true

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(seededRand).Serialize()).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tokenCreate, err := tx.TokenCreate.Create().
		SetTokenIdentifier(randomBytes(32)).
		SetIssuerPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()).
		SetMaxSupply(randomBytes(16)).
		SetTokenName("Test Token").
		SetTokenTicker("TEST").
		SetDecimals(8).
		SetIsFreezable(false).
		SetNetwork(st.NetworkRegtest).
		SetCreationEntityPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()).
		Save(ctx)
	require.NoError(t, err)

	tokenTx, err := tx.TokenTransaction.Create().
		SetPartialTokenTransactionHash(randomBytes(32)).
		SetFinalizedTokenTransactionHash(randomBytes(32)).
		SetStatus(st.TokenTransactionStatusFinalized).
		SetExpiryTime(time.Now().Add(time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Create a token output with the old relationship structure
	// (has output_spent_token_transaction but NOT output_spent_started_token_transactions)
	tokenOutput, err := tx.TokenOutput.Create().
		SetStatus(st.TokenOutputStatusSpentFinalized).
		SetOwnerPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()).
		SetWithdrawBondSats(1000).
		SetWithdrawRelativeBlockLocktime(144).
		SetWithdrawRevocationCommitment(keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()).
		SetTokenPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()).
		SetTokenAmount(randomBytes(8)).
		SetCreatedTransactionOutputVout(0).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetRevocationKeyshare(keyshare).
		SetOutputSpentTokenTransaction(tokenTx). // This is the old single relationship
		Save(ctx)
	require.NoError(t, err)

	// Verify initial state: has single relationship but not M2M
	hasSpentTx, err := tokenOutput.QueryOutputSpentTokenTransaction().Exist(ctx)
	require.NoError(t, err)
	require.True(t, hasSpentTx, "Should have single spent relationship")

	spentStartedCount, err := tokenOutput.QueryOutputSpentStartedTokenTransactions().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, spentStartedCount, "Should not have M2M relationships yet")

	// Get the backfill task from AllStartupTasks
	var backfillTask *StartupTaskSpec
	for _, task := range AllStartupTasks() {
		if task.Name == "backfill_spent_token_transaction_history" {
			backfillTask = &task
			break
		}
	}
	require.NotNil(t, backfillTask, "Should find backfill task")

	err = backfillTask.Task(ctx, config, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err)

	// Verify the M2M relationship was created
	spentStartedTxs, err := tokenOutput.QueryOutputSpentStartedTokenTransactions().All(ctx)
	require.NoError(t, err)
	require.Len(t, spentStartedTxs, 1, "Should have one M2M relationship after backfill")
	require.Equal(t, tokenTx.ID, spentStartedTxs[0].ID, "M2M relationship should point to the same transaction")

	// Verify the original relationship still exists
	hasSpentTx, err = tokenOutput.QueryOutputSpentTokenTransaction().Exist(ctx)
	require.NoError(t, err)
	require.True(t, hasSpentTx, "Should still have single spent relationship")
}
