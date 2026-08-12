//go:build lightspark

package handler

import (
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// requireNotFound asserts the error carries a NOT_FOUND gRPC status rather than
// a bare Go error, since callers branch on the code.
func requireNotFound(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "error must carry a gRPC status")
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestGetUtxo(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	txid, err := chainhash.NewHash(chainhash.DoubleHashB([]byte("get_utxo_txid")))
	require.NoError(t, err)
	txidBytes, err := hex.DecodeString(txid.String())
	require.NoError(t, err)

	testSecretKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testPublicKey := testSecretKey.Public()
	signingKeyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(testSecretKey).
		SetPublicShares(map[string]keys.Public{"test": testPublicKey}).
		SetPublicKey(testPublicKey).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	depositAddress, err := tx.DepositAddress.Create().
		SetAddress("get_utxo_address").
		SetOwnerIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetSigningKeyshare(signingKeyshare).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)

	_, err = tx.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txidBytes).
		SetVout(1).
		SetBlockHeight(500).
		SetAmount(12345).
		SetPkScript([]byte("get_utxo_script")).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	_, err = tx.BlockHeight.Create().
		SetNetwork(btcnetwork.Regtest).
		SetHeight(502).
		Save(ctx)
	require.NoError(t, err)

	_, err = tx.BlockHeight.Create().
		SetNetwork(btcnetwork.Mainnet).
		SetHeight(900).
		Save(ctx)
	require.NoError(t, err)

	handler := NewSspRequestHandler(&so.Config{})

	t.Run("returns the stored record", func(t *testing.T) {
		resp, err := handler.GetUtxo(ctx, &pbssp.GetUtxoRequest{
			Network: pb.Network_REGTEST,
			Txid:    txidBytes,
			Vout:    1,
		})
		require.NoError(t, err)
		require.NotNil(t, resp.GetUtxo())
		assert.Equal(t, txidBytes, resp.GetUtxo().GetTxid())
		assert.Equal(t, uint32(1), resp.GetUtxo().GetVout())
		assert.Equal(t, uint64(12345), resp.GetUtxo().GetAmountSats())
		assert.Equal(t, int64(500), resp.GetUtxo().GetBlockHeight())
		assert.Equal(t, pb.Network_REGTEST, resp.GetUtxo().GetNetwork())
		// The caller derives depth as tip - block_height + 1, here 502 - 500 + 1 = 3.
		assert.Equal(t, int64(502), resp.GetCurrentBlockHeight())
	})

	t.Run("nil request is INVALID_ARGUMENT", func(t *testing.T) {
		resp, err := handler.GetUtxo(ctx, nil)
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok, "error must carry a gRPC status")
		assert.Equal(t, codes.InvalidArgument, st.Code())
		assert.Nil(t, resp)
	})

	t.Run("a network with no block height still reports NOT_FOUND", func(t *testing.T) {
		// Signet has no BlockHeight row here. The utxo lookup must answer first,
		// so the missing row cannot turn an absent utxo into INTERNAL.
		resp, err := handler.GetUtxo(ctx, &pbssp.GetUtxoRequest{
			Network: pb.Network_SIGNET,
			Txid:    txidBytes,
			Vout:    1,
		})
		requireNotFound(t, err)
		assert.Nil(t, resp)
	})

	t.Run("never-ingested utxo is NOT_FOUND", func(t *testing.T) {
		otherTxid, err := chainhash.NewHash(chainhash.DoubleHashB([]byte("never_ingested")))
		require.NoError(t, err)
		otherTxidBytes, err := hex.DecodeString(otherTxid.String())
		require.NoError(t, err)

		resp, err := handler.GetUtxo(ctx, &pbssp.GetUtxoRequest{
			Network: pb.Network_REGTEST,
			Txid:    otherTxidBytes,
			Vout:    0,
		})
		requireNotFound(t, err)
		assert.Nil(t, resp)
	})

	t.Run("a different vout on the same txid is a different utxo", func(t *testing.T) {
		resp, err := handler.GetUtxo(ctx, &pbssp.GetUtxoRequest{
			Network: pb.Network_REGTEST,
			Txid:    txidBytes,
			Vout:    0,
		})
		requireNotFound(t, err)
		assert.Nil(t, resp)
	})

	t.Run("a different network does not match", func(t *testing.T) {
		resp, err := handler.GetUtxo(ctx, &pbssp.GetUtxoRequest{
			Network: pb.Network_MAINNET,
			Txid:    txidBytes,
			Vout:    1,
		})
		requireNotFound(t, err)
		assert.Nil(t, resp)
	})
}
