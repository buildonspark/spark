package task

import (
	"bytes"
	"context"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertStaleRevealedTokenTransaction creates a token transaction in REVEALED status with a
// spent output, backdated past the 5-minute finalization cutoff so the cron selects it. The
// fixture is intentionally not internally finalizable (no partial revocation secret shares), so
// the finalize cron's attempt errors and the task surfaces the transaction's ID in its joined
// error — the observable used to assert which transactions a run attempted.
func insertStaleRevealedTokenTransaction(t *testing.T, ctx context.Context, f *entfixtures.Fixtures) uuid.UUID {
	t.Helper()
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	input := f.CreateStandaloneOutput(tokenCreate, big.NewInt(100))
	tx, _ := f.CreateBalancedTransferTransaction(
		tokenCreate,
		[]*ent.TokenOutput{input},
		entfixtures.OutputSpecs(big.NewInt(100)),
		st.TokenTransactionStatusRevealed,
	)
	_, err := f.Client.TokenTransaction.Update().
		Where(tokentransaction.IDEQ(tx.ID)).
		SetUpdateTime(time.Now().Add(-1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	return tx.ID
}

func findFinalizeRevealedTokenTransactionsTask(t *testing.T) ScheduledTaskSpec {
	t.Helper()
	for _, scheduledTask := range AllScheduledTasks() {
		if scheduledTask.Name == "finalize_revealed_token_transactions" {
			return scheduledTask
		}
	}
	t.Fatal("finalize_revealed_token_transactions task not found")
	return ScheduledTaskSpec{}
}

func idsSortedAscending(ids []uuid.UUID) []uuid.UUID {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return sorted
}

// TestFinalizeRevealedTokenTransactions_RespectsBatchLimitAndKeysetOrder inserts more stale
// REVEALED transactions than the batch limit and pins two invariants in one run: the run is
// bounded to a single batch (batch_limit=2, max_runtime_seconds=0 stops after the first chunk),
// and the batch is the lowest transactions by the keyset-ordered (UUIDv7) primary key.
func TestFinalizeRevealedTokenTransactions_RespectsBatchLimitAndKeysetOrder(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)
	f := entfixtures.New(t, ctx, client)

	const total = 5
	ids := make([]uuid.UUID, total)
	for i := range total {
		ids[i] = insertStaleRevealedTokenTransaction(t, ctx, f)
	}
	ordered := idsSortedAscending(ids)

	task := findFinalizeRevealedTokenTransactionsTask(t)
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobFinalizeRevealedTokenTransactionsBatchLimit:        2,
		knobs.KnobFinalizeRevealedTokenTransactionsMaxRuntimeSeconds: 0,
	})

	err := task.RunOnce(ctx, cfg, client, nil, fixedKnobs)
	require.Error(t, err, "finalize attempts fail without peer operators and surface per-tx errors")

	msg := err.Error()
	assert.Contains(t, msg, ordered[0].String(), "lowest id should be in the first batch")
	assert.Contains(t, msg, ordered[1].String(), "second-lowest id should be in the first batch")
	for _, id := range ordered[2:] {
		assert.NotContains(t, msg, id.String(), "transaction past the batch limit should be untouched")
	}
}

// TestFinalizeRevealedTokenTransactions_DrainsAcrossBatchesWithinBudget is the companion: with a
// generous runtime budget the run keeps draining batch-after-batch until the backlog is empty,
// so every stale transaction is attempted in a single run even though batch_limit is smaller.
func TestFinalizeRevealedTokenTransactions_DrainsAcrossBatchesWithinBudget(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)
	f := entfixtures.New(t, ctx, client)

	const total = 5
	ids := make([]uuid.UUID, total)
	for i := range total {
		ids[i] = insertStaleRevealedTokenTransaction(t, ctx, f)
	}

	task := findFinalizeRevealedTokenTransactionsTask(t)
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobFinalizeRevealedTokenTransactionsBatchLimit:        2,
		knobs.KnobFinalizeRevealedTokenTransactionsMaxRuntimeSeconds: 300,
	})

	err := task.RunOnce(ctx, cfg, client, nil, fixedKnobs)
	require.Error(t, err)

	msg := err.Error()
	for _, id := range ids {
		assert.Contains(t, msg, id.String(), "every stale transaction should be drained within the budget")
	}
}
