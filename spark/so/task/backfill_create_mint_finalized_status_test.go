package task

import (
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func getBackfillCreateMintFinalizedStatusTask() (StartupTaskSpec, error) {
	for _, t := range AllStartupTasks() {
		if t.Name == "backfill_create_mint_finalized_status" {
			return t, nil
		}
	}
	return StartupTaskSpec{}, assert.AnError
}

func TestBackfillCreateMintFinalizedStatus(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	f := entfixtures.New(t, ctx, client)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	tests := []struct {
		name           string
		setupTx        func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction
		shouldUpdate   bool
		expectedStatus st.TokenTransactionStatus
		description    string
	}{
		{
			name: "v3 mint with all outputs CREATED_FINALIZED should update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestMintTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
					st.TokenOutputStatusCreatedFinalized,
				})
			},
			shouldUpdate:   true,
			expectedStatus: st.TokenTransactionStatusFinalized,
			description:    "v3 mint with all CREATED_FINALIZED outputs",
		},
		{
			name: "v3 create with all outputs CREATED_FINALIZED should update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestCreateTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
				})
			},
			shouldUpdate:   true,
			expectedStatus: st.TokenTransactionStatusFinalized,
			description:    "v3 create with all CREATED_FINALIZED outputs",
		},
		{
			name: "v3 mint with SPENT outputs should update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestMintTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusSpentStarted,
					st.TokenOutputStatusSpentSigned,
					st.TokenOutputStatusSpentFinalized,
				})
			},
			shouldUpdate:   true,
			expectedStatus: st.TokenTransactionStatusFinalized,
			description:    "v3 mint with all SPENT_* outputs",
		},
		{
			name: "v3 mint with mixed valid states should update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestMintTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
					st.TokenOutputStatusSpentFinalized,
				})
			},
			shouldUpdate:   true,
			expectedStatus: st.TokenTransactionStatusFinalized,
			description:    "v3 mint with mixed CREATED_FINALIZED and SPENT_FINALIZED",
		},
		{
			name: "v3 mint with CREATED_STARTED output should NOT update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestMintTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
					st.TokenOutputStatusCreatedStarted,
				})
			},
			shouldUpdate:   false,
			expectedStatus: st.TokenTransactionStatusSigned,
			description:    "v3 mint with one CREATED_STARTED output",
		},
		{
			name: "v2 mint should NOT update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestMintTransaction(t, f, tokenCreate, 2, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
				})
			},
			shouldUpdate:   false,
			expectedStatus: st.TokenTransactionStatusSigned,
			description:    "v2 mint should be filtered out by version",
		},
		{
			name: "v0 mint should NOT update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestMintTransaction(t, f, tokenCreate, 0, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
				})
			},
			shouldUpdate:   false,
			expectedStatus: st.TokenTransactionStatusSigned,
			description:    "v0 mint should be filtered out by version",
		},
		{
			name: "v3 mint already FINALIZED should remain FINALIZED",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestMintTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusFinalized, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
				})
			},
			shouldUpdate:   false,
			expectedStatus: st.TokenTransactionStatusFinalized,
			description:    "already FINALIZED transaction (idempotent)",
		},
		{
			name: "v3 create with no outputs should update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestCreateTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{})
			},
			shouldUpdate:   true,
			expectedStatus: st.TokenTransactionStatusFinalized,
			description:    "create with no created outputs (creates don't use CreatedOutput edge)",
		},
		{
			name: "v3 transfer (no mint/create) should NOT update",
			setupTx: func(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate) *ent.TokenTransaction {
				return createTestTransferTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
					st.TokenOutputStatusCreatedFinalized,
				})
			},
			shouldUpdate:   false,
			expectedStatus: st.TokenTransactionStatusSigned,
			description:    "transfer transaction (not mint/create)",
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seed := [32]byte{byte(i)}
			rng := rand.NewChaCha8(seed)
			testFixtures := entfixtures.New(t, ctx, client).WithRNG(rng)
			tx := tc.setupTx(t, testFixtures, tokenCreate)

			backfillTask, err := getBackfillCreateMintFinalizedStatusTask()
			require.NoError(t, err)

			// Enable the knob
			knobsService := knobs.NewFixedKnobs(map[string]float64{
				knobs.KnobBackfillCreateMintFinalizedStatusEnabled: 100,
			})

			err = backfillTask.RunOnce(ctx, cfg, client, knobsService)
			require.NoError(t, err)

			// Verify the transaction status
			updatedTx, err := client.TokenTransaction.Query().
				Where(tokentransaction.IDEQ(tx.ID)).
				Only(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedStatus, updatedTx.Status, tc.description)
		})
	}
}

func TestBackfillCreateMintFinalizedStatus_KnobDisabled(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client
	cfg := sparktesting.TestConfig(t)

	f := entfixtures.New(t, ctx, client)
	tokenCreate := f.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	tx := createTestMintTransaction(t, f, tokenCreate, 3, st.TokenTransactionStatusSigned, []st.TokenOutputStatus{
		st.TokenOutputStatusCreatedFinalized,
	})

	backfillTask, err := getBackfillCreateMintFinalizedStatusTask()
	require.NoError(t, err)

	// Disable the knob
	knobsService := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobBackfillCreateMintFinalizedStatusEnabled: 0,
	})

	err = backfillTask.RunOnce(ctx, cfg, client, knobsService)
	require.NoError(t, err)

	// Transaction should remain SIGNED
	updatedTx, err := client.TokenTransaction.Query().
		Where(tokentransaction.IDEQ(tx.ID)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TokenTransactionStatusSigned, updatedTx.Status, "should not update when knob disabled")
}

// Helper functions to create test data using entfixtures

func createTestMintTransaction(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate, version int, status st.TokenTransactionStatus, outputStatuses []st.TokenOutputStatus) *ent.TokenTransaction {
	outputSpecs := make([]entfixtures.OutputSpec, len(outputStatuses))
	for i := range outputSpecs {
		outputSpecs[i] = entfixtures.OutputSpec{
			Amount: big.NewInt(100),
		}
	}

	tx, outputs := f.CreateMintTransactionWithOpts(tokenCreate, outputSpecs, status, &entfixtures.TokenTransactionOpts{})

	tx, err := tx.Update().
		SetVersion(st.TokenTransactionVersion(version)).
		Save(f.Ctx)
	require.NoError(t, err)

	for i, output := range outputs {
		if i < len(outputStatuses) {
			_, err := output.Update().
				SetStatus(outputStatuses[i]).
				Save(f.Ctx)
			require.NoError(t, err)
		}
	}

	return tx
}

func createTestCreateTransaction(t *testing.T, f *entfixtures.Fixtures, baseTokenCreate *ent.TokenCreate, version int, status st.TokenTransactionStatus, outputStatuses []st.TokenOutputStatus) *ent.TokenTransaction {
	_, tokenCreate := f.CreateTokenCreateWithIssuer(baseTokenCreate.Network, nil, nil)

	tx := f.CreateCreateTransaction(tokenCreate, status, &entfixtures.TokenTransactionOpts{})

	tx, err := tx.Update().
		SetVersion(st.TokenTransactionVersion(version)).
		Save(f.Ctx)
	require.NoError(t, err)

	for i, outputStatus := range outputStatuses {
		output := f.CreateOutputForTransaction(tokenCreate, big.NewInt(100), tx, int32(i))
		_, err := output.Update().
			SetStatus(outputStatus).
			Save(f.Ctx)
		require.NoError(t, err)
	}

	return tx
}

func createTestTransferTransaction(t *testing.T, f *entfixtures.Fixtures, tokenCreate *ent.TokenCreate, version int, status st.TokenTransactionStatus, outputStatuses []st.TokenOutputStatus) *ent.TokenTransaction {
	tx, err := f.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(f.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(f.RandomBytes(32)).
		SetStatus(status).
		SetVersion(st.TokenTransactionVersion(version)).
		Save(f.Ctx)
	require.NoError(t, err)

	for i, outputStatus := range outputStatuses {
		output := f.CreateOutputForTransaction(tokenCreate, big.NewInt(100), tx, int32(i))
		_, err := output.Update().
			SetStatus(outputStatus).
			Save(f.Ctx)
		require.NoError(t, err)
	}

	return tx
}
