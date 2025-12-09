package entexample_test

import (
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent/entexample"
	"github.com/stretchr/testify/require"
)

func TestBlockHeightExample_DefaultValues(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	// Create a block height with all default values
	blockHeight := entexample.NewBlockHeightExample(t, tc.Client).
		MustExec(ctx)

	require.NotNil(t, blockHeight)
	require.Equal(t, int64(100), blockHeight.Height)
	require.Equal(t, btcnetwork.Regtest, blockHeight.Network)
}

func TestBlockHeightExample_CustomValues(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	// Create a block height with custom values
	blockHeight := entexample.NewBlockHeightExample(t, tc.Client).
		SetHeight(850000).
		SetNetwork(btcnetwork.Mainnet).
		MustExec(ctx)

	require.NotNil(t, blockHeight)
	require.Equal(t, int64(850000), blockHeight.Height)
	require.Equal(t, btcnetwork.Mainnet, blockHeight.Network)
}

func TestBlockHeightExample_PartialOverride(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	// Create a block height with only height overridden, network uses default
	blockHeight := entexample.NewBlockHeightExample(t, tc.Client).
		SetHeight(123456).
		MustExec(ctx)

	require.NotNil(t, blockHeight)
	require.Equal(t, int64(123456), blockHeight.Height)
	require.Equal(t, btcnetwork.Regtest, blockHeight.Network)
}

func TestBlockHeightExample_ExecReturnsError(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	// Use Exec() instead of MustExec() to get error handling
	blockHeight, err := entexample.NewBlockHeightExample(t, tc.Client).
		SetHeight(999999).
		SetNetwork(btcnetwork.Regtest).
		Exec(ctx)

	// This should succeed
	require.NoError(t, err)
	require.NotNil(t, blockHeight)
	require.Equal(t, int64(999999), blockHeight.Height)
	require.Equal(t, btcnetwork.Regtest, blockHeight.Network)
}
