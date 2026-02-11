package tokens

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type internalFinalizeTokenPostgresTestSetup struct {
	handler  *InternalFinalizeTokenHandler
	ctx      context.Context
	client   *ent.Client
	fixtures *entfixtures.Fixtures
}

func setUpInternalFinalizeTokenTestHandlerPostgres(t *testing.T) *internalFinalizeTokenPostgresTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	return &internalFinalizeTokenPostgresTestSetup{
		handler:  NewInternalFinalizeTokenHandler(config),
		ctx:      ctx,
		client:   dbClient,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
}

func TestFinalizeMintOrCreateTransaction(t *testing.T) {
	setup := setUpInternalFinalizeTokenTestHandlerPostgres(t)

	t.Run("non-expired MINT finalization succeeds because already counted in supply", func(t *testing.T) {
		maxSupply := big.NewInt(200)
		tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, maxSupply)

		// Create a FINALIZED mint that already used 150 of the 200 max supply
		setup.fixtures.CreateMintTransaction(
			tokenCreate,
			entfixtures.OutputSpecs(big.NewInt(150)),
			st.TokenTransactionStatusFinalized,
		)

		// Create a non-expired SIGNED mint for 100 more — numerically this would bring
		// total to 250 exceeding 200, but non-expired SIGNED transactions are already
		// counted in the current supply calculation, so finalization is a no-op for
		// supply totals and should succeed.
		tx2, _ := setup.fixtures.CreateMintTransaction(
			tokenCreate,
			entfixtures.OutputSpecs(big.NewInt(100)),
			st.TokenTransactionStatusSigned,
		)

		// Reload with edges for finalization
		tx2Loaded, err := setup.client.TokenTransaction.Query().
			Where(tokentransaction.IDEQ(tx2.ID)).
			WithMint().
			WithCreate().
			WithCreatedOutput().
			WithSpentOutput().
			Only(setup.ctx)
		require.NoError(t, err)

		err = setup.handler.FinalizeMintOrCreateTransaction(setup.ctx, tx2Loaded)
		require.NoError(t, err)

		updated, err := setup.client.TokenTransaction.Get(setup.ctx, tx2.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenTransactionStatusFinalized, updated.Status)
	})

	t.Run("MINT transaction finalization succeeds within max supply", func(t *testing.T) {
		// Use a fresh token so we don't carry over state from the previous subtest
		freshTokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, big.NewInt(500))

		// Create a FINALIZED mint using 100 of 500
		setup.fixtures.CreateMintTransaction(
			freshTokenCreate,
			entfixtures.OutputSpecs(big.NewInt(100)),
			st.TokenTransactionStatusFinalized,
		)

		// Create a SIGNED mint for 100 more — total 200, well within 500
		tx, _ := setup.fixtures.CreateMintTransaction(
			freshTokenCreate,
			entfixtures.OutputSpecs(big.NewInt(100)),
			st.TokenTransactionStatusSigned,
		)

		txLoaded, err := setup.client.TokenTransaction.Query().
			Where(tokentransaction.IDEQ(tx.ID)).
			WithMint().
			WithCreate().
			WithCreatedOutput().
			WithSpentOutput().
			Only(setup.ctx)
		require.NoError(t, err)

		err = setup.handler.FinalizeMintOrCreateTransaction(setup.ctx, txLoaded)
		require.NoError(t, err)

		updated, err := setup.client.TokenTransaction.Get(setup.ctx, tx.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenTransactionStatusFinalized, updated.Status)
	})

	t.Run("expired MINT finalization succeeds when within max supply", func(t *testing.T) {
		freshTokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, big.NewInt(300))

		// Create a FINALIZED mint using 100 of 300
		setup.fixtures.CreateMintTransaction(
			freshTokenCreate,
			entfixtures.OutputSpecs(big.NewInt(100)),
			st.TokenTransactionStatusFinalized,
		)

		// Create an expired SIGNED mint for 100 more — total 200, within 300
		pastTime := time.Now().Add(-time.Hour)
		tx, _ := setup.fixtures.CreateMintTransactionWithOpts(
			freshTokenCreate,
			entfixtures.OutputSpecs(big.NewInt(100)),
			st.TokenTransactionStatusSigned,
			&entfixtures.TokenTransactionOpts{ExpiryTime: &pastTime},
		)

		txLoaded, err := setup.client.TokenTransaction.Query().
			Where(tokentransaction.IDEQ(tx.ID)).
			WithMint().
			WithCreate().
			WithCreatedOutput().
			WithSpentOutput().
			Only(setup.ctx)
		require.NoError(t, err)

		err = setup.handler.FinalizeMintOrCreateTransaction(setup.ctx, txLoaded)
		require.NoError(t, err)

		updated, err := setup.client.TokenTransaction.Get(setup.ctx, tx.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenTransactionStatusFinalized, updated.Status)
	})

	t.Run("expired MINT finalization fails when exceeding max supply", func(t *testing.T) {
		freshTokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, big.NewInt(300))

		// Create a FINALIZED mint using 250 of 300
		setup.fixtures.CreateMintTransaction(
			freshTokenCreate,
			entfixtures.OutputSpecs(big.NewInt(250)),
			st.TokenTransactionStatusFinalized,
		)

		// Create an expired SIGNED mint for 100 more — total 350, exceeds 300
		pastTime := time.Now().Add(-time.Hour)
		tx, _ := setup.fixtures.CreateMintTransactionWithOpts(
			freshTokenCreate,
			entfixtures.OutputSpecs(big.NewInt(100)),
			st.TokenTransactionStatusSigned,
			&entfixtures.TokenTransactionOpts{ExpiryTime: &pastTime},
		)

		txLoaded, err := setup.client.TokenTransaction.Query().
			Where(tokentransaction.IDEQ(tx.ID)).
			WithMint().
			WithCreate().
			WithCreatedOutput().
			WithSpentOutput().
			Only(setup.ctx)
		require.NoError(t, err)

		err = setup.handler.FinalizeMintOrCreateTransaction(setup.ctx, txLoaded)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max supply")
	})

	t.Run("CREATE finalization succeeds", func(t *testing.T) {
		createTokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
		createTx := setup.fixtures.CreateCreateTransaction(
			createTokenCreate,
			st.TokenTransactionStatusSigned,
			&entfixtures.TokenTransactionOpts{},
		)

		createTxLoaded, err := setup.client.TokenTransaction.Query().
			Where(tokentransaction.IDEQ(createTx.ID)).
			WithMint().
			WithCreate().
			WithCreatedOutput().
			WithSpentOutput().
			Only(setup.ctx)
		require.NoError(t, err)

		err = setup.handler.FinalizeMintOrCreateTransaction(setup.ctx, createTxLoaded)
		require.NoError(t, err)

		updated, err := setup.client.TokenTransaction.Get(setup.ctx, createTx.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenTransactionStatusFinalized, updated.Status)
	})
}
