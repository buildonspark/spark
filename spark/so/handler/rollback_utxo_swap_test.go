package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/distributed-lab/gripmock"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

func TestRollbackUtxoSwap_InvalidStatement(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	// Create request with invalid signature
	req := &pbinternal.RollbackUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    []byte("test_txid"),
			Vout:    0,
			Network: pb.Network_REGTEST,
		},
		Signature:            []byte("invalid_signature"),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
	}

	_, err := handler.RollbackUtxoSwap(ctx, cfg, req)
	require.ErrorContains(t, err, "signature")
}

func TestRollbackUtxoSwap_UtxoDoesNotExist(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	// Generate valid rollback request for non-existent UTXO
	nonExistentTxid := chainhash.DoubleHashB([]byte("nonexistent_txid_for_testing_12345"))
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    nonExistentTxid,
		Vout:    0,
		Network: pb.Network_REGTEST,
	})
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.ErrorContains(t, err, "not found")
}

func TestRollbackUtxoSwap_NoErrorIfUtxoSwapDoesNotExist(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	err := gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil)
	require.NoError(t, err)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Don't create UtxoSwap - it doesn't exist
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	})
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.NoError(t, err) // Should not error if UtxoSwap doesn't exist
}

func TestRollbackUtxoSwap_NoErrorIfUtxoSwapCancelled(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	err := gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil)
	require.NoError(t, err)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create cancelled UtxoSwap
	_ = createTestUtxoSwap(t, ctx, rng, sessionCtx.Client, utxo, st.UtxoSwapStatusCancelled)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	})
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.NoError(t, err) // Should not error for cancelled UtxoSwap
}

func TestRollbackUtxoSwap_NoErrorIfUtxoSwapCreated(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	err := gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil)
	require.NoError(t, err)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetUserIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		Save(ctx)
	require.NoError(t, err)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	})
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.NoError(t, err)

	// Commit tx before checking the result
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Verify UtxoSwap is now cancelled (use fresh context)
	updatedUtxoSwap, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedUtxoSwap.Status)
}

func TestRollbackUtxoSwap_ErrorIfUtxoSwapCompleted(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create completed UtxoSwap
	_, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetUtxo(utxo).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetUserIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		Save(ctx)
	require.NoError(t, err)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	})
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.ErrorContains(t, err, "completed")
}
