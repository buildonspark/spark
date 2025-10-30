package task

import (
	"bytes"
	"context"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uint128"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestBackfillSpentTokenTransactionHistory(t *testing.T) {
	seededRand := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	config := sparktesting.TestConfig(t)
	config.Token.EnableBackfillSpentTokenTransactionHistoryTask = true

	f := entfixtures.New(t, ctx, tx).WithRNG(seededRand)

	keyshare := f.CreateKeyshare()
	tokenCreate := f.CreateTokenCreate(st.NetworkRegtest, nil, nil)

	tokenTx, err := tx.TokenTransaction.Create().
		SetPartialTokenTransactionHash(f.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(f.RandomBytes(32)).
		SetStatus(st.TokenTransactionStatusSigned).
		SetExpiryTime(time.Now().Add(time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Create a token output with the old relationship structure
	// (has output_spent_token_transaction but NOT output_spent_started_token_transactions)
	inputAmount := new(big.Int).SetBytes(f.RandomBytes(8))
	tokenOutput, err := tx.TokenOutput.Create().
		SetStatus(st.TokenOutputStatusSpentFinalized).
		SetOwnerPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public()).
		SetWithdrawBondSats(1000000).
		SetWithdrawRelativeBlockLocktime(1000).
		SetWithdrawRevocationCommitment(keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()).
		SetTokenPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public()).
		SetTokenAmount(inputAmount.Bytes()).
		SetCreatedTransactionOutputVout(0).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetRevocationKeyshare(keyshare).
		SetOutputSpentTokenTransaction(tokenTx). // This is the old single relationship
		Save(ctx)
	require.NoError(t, err)

	outputKeyshare := f.CreateKeyshare()

	_, err = tx.TokenOutput.Create().
		SetStatus(st.TokenOutputStatusCreatedFinalized).
		SetOwnerPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public()).
		SetWithdrawBondSats(1000000).
		SetWithdrawRelativeBlockLocktime(1000).
		SetWithdrawRevocationCommitment(keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()).
		SetTokenPublicKey(keys.MustGeneratePrivateKeyFromRand(seededRand).Public()).
		SetTokenAmount(inputAmount.Bytes()).
		SetCreatedTransactionOutputVout(0).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetRevocationKeyshare(outputKeyshare).
		SetOutputCreatedTokenTransaction(tokenTx).
		Save(ctx)
	require.NoError(t, err)

	tokenTx, err = tokenTx.Update().
		SetStatus(st.TokenTransactionStatusFinalized).
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

func TestBackfillTreeNodeTxids(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	nodeID := uuid.New()
	treeID := uuid.New()

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubkey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubkey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	nodeValue := uint64(1000)
	nodeVerifyingPubkey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	nodeRawTx, err := sparktesting.CreateTestP2TRTransaction("bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", 1000)
	require.NoError(t, err)
	nodeRawTxBytes, err := common.SerializeTx(nodeRawTx)
	require.NoError(t, err)
	nodeRawRefundTx, err := sparktesting.CreateTestP2TRTransaction("bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", 1000)
	require.NoError(t, err)
	nodeRawRefundTxBytes, err := common.SerializeTx(nodeRawRefundTx)
	require.NoError(t, err)
	nodeDirectRefundTx, err := sparktesting.CreateTestP2TRTransaction("bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", 1000)
	require.NoError(t, err)
	nodeDirectRefundTxBytes, err := common.SerializeTx(nodeDirectRefundTx)
	require.NoError(t, err)
	nodeDirectFromCpfpRefundTx, err := sparktesting.CreateTestP2TRTransaction("bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", 1000)
	require.NoError(t, err)
	nodeDirectFromCpfpRefundTxBytes, err := common.SerializeTx(nodeDirectFromCpfpRefundTx)
	require.NoError(t, err)

	_, err = tx.Tree.Create().
		SetID(treeID).
		SetOwnerIdentityPubkey(ownerIdentityPubkey).
		SetNetwork(st.NetworkRegtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid([]byte{1, 2, 3}).
		SetVout(int16(0)).
		Save(ctx)
	require.NoError(t, err)

	keysharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingKeyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"test": publicSharePrivkey.Public()}).
		SetPublicKey(keysharePrivkey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// OpCreate
	insertQuery := `
		INSERT INTO tree_nodes (id, create_time, update_time, tree_node_tree, status, owner_identity_pubkey, owner_signing_pubkey, value, verifying_pubkey, tree_node_signing_keyshare, raw_tx, raw_refund_tx, direct_refund_tx, direct_from_cpfp_refund_tx, vout)
		VALUES ($1, '2025-01-01 00:00:00', '2025-01-01 00:00:00', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`
	// Bypass hooks
	// nolint:forbidigo
	_, err = tx.ExecContext(ctx, insertQuery, nodeID, treeID, st.TreeNodeStatusAvailable, ownerIdentityPubkey.Serialize(), ownerSigningPubkey.Serialize(), nodeValue, nodeVerifyingPubkey.Serialize(), signingKeyshare.ID, nodeRawTxBytes, nodeRawRefundTxBytes, nodeDirectRefundTxBytes, nodeDirectFromCpfpRefundTxBytes, int16(0))
	require.NoError(t, err)

	// Get the backfill task from AllStartupTasks
	var backfillTask *ScheduledTaskSpec
	for _, task := range AllScheduledTasks() {
		if task.Name == "backfill_tree_node_txids" {
			backfillTask = &task
			break
		}
	}
	require.NotNil(t, backfillTask, "Should find backfill task")

	config := sparktesting.TestConfig(t)

	err = backfillTask.Task(ctx, config, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err)

	tx, err = ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	treeNode, err := tx.TreeNode.Query().
		Where(treenode.ID(nodeID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, treeNode.RawTxid)
	require.True(t, bytes.Equal(treeNode.RawTx, nodeRawTxBytes))
	require.NotNil(t, treeNode.RawRefundTxid)
	require.NotNil(t, treeNode.DirectRefundTxid)
	require.NotNil(t, treeNode.DirectFromCpfpRefundTxid)
}

func TestBackfillTokenOutputAmount_SQLite(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	runBackfillAmountTest(t, ctx, tx)
}

func TestBackfillTokenOutputAmount_Postgres(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	runBackfillAmountTest(t, ctx, tx)
}

func runBackfillAmountTest(t *testing.T, ctx context.Context, tx *ent.Tx) {
	t.Helper()

	seededRand := rand.NewChaCha8([32]byte{})
	k := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTasksEnableBackfillTokenOutputAmountBatchSize: 5000,
	})

	var backfillTask *ScheduledTaskSpec
	for _, task := range AllScheduledTasks() {
		if task.Name == "backfill_token_output_amounts" {
			backfillTask = &task
			break
		}
	}
	require.NotNil(t, backfillTask, "Should find backfill task")

	config := sparktesting.TestConfig(t)

	f := entfixtures.New(t, ctx, tx).WithRNG(seededRand)

	tokenAmountBytes := f.RandomBytes(16)
	tokenAmountBytes[15] |= 1 // force non zero random value

	tokenCreate := f.CreateTokenCreate(st.NetworkRegtest, nil, nil)

	_, outputs := f.CreateMintTransaction(tokenCreate,
		entfixtures.OutputSpecs(new(big.Int).SetBytes(tokenAmountBytes)),
		st.TokenTransactionStatusSigned)
	require.Len(t, outputs, 1)

	require.NoError(t, backfillTask.Task(ctx, config, k))

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	out, err := tx.TokenOutput.Query().Where(tokenoutput.ID(outputs[0].ID)).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, out.Amount)

	u128Amount := uint128.Uint128{}
	err = u128Amount.SafeSetBytes(tokenAmountBytes)
	require.NoError(t, err)
	require.Equal(t, u128Amount, out.Amount)
}
