package handler

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func testDepositConfirmationConfig() *so.Config {
	return &so.Config{
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {
				DepositConfirmationThreshold: 3,
			},
		},
	}
}

func serializeTestTx(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return buf.Bytes()
}

func testConfirmedDepositAddressWithoutAvailabilityFlag(
	t *testing.T,
) (context.Context, *ent.Client, *ent.DepositAddress, *wire.MsgTx) {
	t.Helper()

	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	_, err = client.BlockHeight.Create().
		SetNetwork(btcnetwork.Regtest).
		SetHeight(200).
		Save(ctx)
	require.NoError(t, err)

	onChainTx := wire.NewMsgTx(2)
	onChainTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{}})
	onChainTx.AddTxOut(&wire.TxOut{Value: 1_000, PkScript: []byte{0x51, 0x20, 0x00}})

	depositAddress := createTestDepositAddress(t, ctx, client)
	depositAddress, err = depositAddress.Update().
		SetConfirmationTxid(onChainTx.TxHash().String()).
		SetConfirmationHeight(198).
		Save(ctx)
	require.NoError(t, err)

	depositAddress, err = client.DepositAddress.Query().
		Where(depositaddress.ID(depositAddress.ID)).
		WithSigningKeyshare().
		Only(ctx)
	require.NoError(t, err)

	return ctx, client, depositAddress, onChainTx
}

func TestIsDepositUtxoAvailableForTreeCreation_UsesConfirmationHeightWhenAvailabilityFlagIsStale(t *testing.T) {
	ctx, _, depositAddress, onChainTx := testConfirmedDepositAddressWithoutAvailabilityFlag(t)

	confirmed, err := isDepositUtxoAvailableForTreeCreation(
		ctx,
		testDepositConfirmationConfig(),
		btcnetwork.Regtest,
		depositAddress,
		onChainTx,
		0,
	)
	require.NoError(t, err)
	require.True(t, confirmed)
}

func TestCreateTreeAndNode_SetsAvailableWhenConfirmationHeightIsMatureButAvailabilityFlagIsUnset(t *testing.T) {
	ctx, _, depositAddress, onChainTx := testConfirmedDepositAddressWithoutAvailabilityFlag(t)

	tree, node, err := createTreeAndNode(
		ctx,
		testDepositConfirmationConfig(),
		depositAddress,
		onChainTx,
		onChainTx.TxOut[0],
		nil,
		0,
		btcnetwork.Regtest,
		keys.GeneratePrivateKey().Public(),
		serializeTestTx(t, onChainTx),
		serializeTestTx(t, onChainTx),
		serializeTestTx(t, onChainTx),
	)
	require.NoError(t, err)
	require.Equal(t, st.TreeStatusAvailable, tree.Status)
	require.Equal(t, st.TreeNodeStatusAvailable, node.Status)
}
