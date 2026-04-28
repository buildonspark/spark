package handler

import (
	"bytes"
	"context"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/distributed-lab/gripmock"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
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
	}, nil)
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
	}, nil)
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
	}, nil)
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
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.NoError(t, err)

	// Commit tx before checking the result
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

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
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.ErrorContains(t, err, "completed")
}

// --- Helpers for RollbackInstantUtxoSwap tests ---

// createTestP2TRTx creates a version-3 transaction that pays to a P2TR address derived from pubKey.
// Returns the serialized raw tx bytes, the txid hash, and the P2TR address string.
func createTestP2TRTx(t *testing.T, pubKey keys.Public, amount int64, network btcnetwork.Network) ([]byte, chainhash.Hash, string) {
	t.Helper()

	p2trScript, err := common.P2TRScriptFromPubKey(pubKey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(3)
	prevHash, _ := chainhash.NewHashFromStr("1111111111111111111111111111111111111111111111111111111111111111")
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: *prevHash, Index: 0},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	tx.AddTxOut(&wire.TxOut{
		Value:    amount,
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	require.NoError(t, err)

	// Derive the address string the same way the handler does
	addr, err := common.P2TRRawAddressFromPublicKey(pubKey, network)
	require.NoError(t, err)

	return buf.Bytes(), tx.TxHash(), addr.String()
}

// generateRollbackInstantRequest creates a valid RollbackInstantUtxoSwapRequest signed by the config's identity key.
func generateRollbackInstantRequest(
	t *testing.T,
	ctx context.Context,
	cfg *so.Config,
	utxo *pb.UTXO,
	rawTx []byte,
	rollbackFrom []pb.UtxoSwapStatus,
	rollbackTo pb.UtxoSwapStatus,
) *pbinternal.RollbackInstantUtxoSwapRequest {
	t.Helper()

	// Reuse the existing helper which generates the correct signature
	baseReq, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, utxo, nil)
	require.NoError(t, err)

	return &pbinternal.RollbackInstantUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: utxo.Network,
			RawTx:   rawTx,
		},
		Signature:            baseReq.Signature,
		CoordinatorPublicKey: baseReq.CoordinatorPublicKey,
		RollbackFromStatuses: rollbackFrom,
		RollbackToStatus:     rollbackTo,
	}
}

// --- RollbackInstantUtxoSwap tests ---

func TestRollbackInstantUtxoSwap_InvalidRollbackFromStatus(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	txid := chainhash.DoubleHashB([]byte("test_txid_for_instant_rollback"))
	req := &pbinternal.RollbackInstantUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    txid,
			Vout:    0,
			Network: pb.Network_REGTEST,
		},
		Signature:            []byte("placeholder"),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
		RollbackFromStatuses: []pb.UtxoSwapStatus{pb.UtxoSwapStatus_UTXO_SWAP_STATUS_UNSPECIFIED},
		RollbackToStatus:     pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CANCELLED,
	}

	_, err := handler.RollbackInstantUtxoSwap(ctx, cfg, req)
	require.ErrorContains(t, err, "invalid rollback_from_status")
}

func TestRollbackInstantUtxoSwap_InvalidRollbackToStatus(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	txid := chainhash.DoubleHashB([]byte("test_txid_for_instant_rollback"))
	req := &pbinternal.RollbackInstantUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    txid,
			Vout:    0,
			Network: pb.Network_REGTEST,
		},
		Signature:            []byte("placeholder"),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
		RollbackFromStatuses: []pb.UtxoSwapStatus{pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CREATED},
		RollbackToStatus:     pb.UtxoSwapStatus_UTXO_SWAP_STATUS_UNSPECIFIED,
	}

	_, err := handler.RollbackInstantUtxoSwap(ctx, cfg, req)
	require.ErrorContains(t, err, "invalid rollback_to_status")
}

func TestRollbackInstantUtxoSwap_InvalidSignature(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	txid := chainhash.DoubleHashB([]byte("test_txid_for_instant_rollback"))
	req := &pbinternal.RollbackInstantUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    txid,
			Vout:    0,
			Network: pb.Network_REGTEST,
		},
		Signature:            []byte("invalid_signature"),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
		RollbackFromStatuses: []pb.UtxoSwapStatus{pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CREATED},
		RollbackToStatus:     pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CANCELLED,
	}

	_, err := handler.RollbackInstantUtxoSwap(ctx, cfg, req)
	require.ErrorContains(t, err, "signature")
}

func TestRollbackInstantUtxoSwap_NoErrorIfUtxoSwapDoesNotExist(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{1})
	depositKeyPair := keys.MustGeneratePrivateKeyFromRand(rng)

	const txAmount int64 = 50000
	rawTx, txHash, addrStr := createTestP2TRTx(t, depositKeyPair.Public(), txAmount, btcnetwork.Regtest)

	// Create deposit address with matching address string
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress, err := sessionCtx.Client.DepositAddress.Create().
		SetAddress(addrStr).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetSigningKeyshare(keyshare).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)

	// Create the UTXO in the DB so VerifiedTargetUtxoFromRequest finds it
	_, err = sessionCtx.Client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txHash[:]).
		SetVout(0).
		SetBlockHeight(50).
		SetAmount(uint64(txAmount)).
		SetPkScript([]byte("test_pk_script")).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	// No UtxoSwap created — function should return empty response without error
	req := generateRollbackInstantRequest(t, ctx, cfg, &pb.UTXO{
		Txid:    txHash[:],
		Vout:    0,
		Network: pb.Network_REGTEST,
	}, rawTx, []pb.UtxoSwapStatus{pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CREATED}, pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CANCELLED)

	resp, err := handler.RollbackInstantUtxoSwap(ctx, cfg, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestRollbackInstantUtxoSwap_SuccessfulRollback(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{2})
	depositKeyPair := keys.MustGeneratePrivateKeyFromRand(rng)

	const txAmount int64 = 50000
	rawTx, txHash, addrStr := createTestP2TRTx(t, depositKeyPair.Public(), txAmount, btcnetwork.Regtest)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress, err := sessionCtx.Client.DepositAddress.Create().
		SetAddress(addrStr).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetSigningKeyshare(keyshare).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)

	// Create the UTXO
	_, err = sessionCtx.Client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txHash[:]).
		SetVout(0).
		SetBlockHeight(50).
		SetAmount(uint64(txAmount)).
		SetPkScript([]byte("test_pk_script")).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	// Create UtxoSwap in CREATED status, linked to deposit address
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxoValueSats(uint64(txAmount)).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	// Link UtxoSwap to DepositAddress
	_, err = depositAddress.Update().AddUtxoswaps(utxoSwap).Save(ctx)
	require.NoError(t, err)

	req := generateRollbackInstantRequest(t, ctx, cfg, &pb.UTXO{
		Txid:    txHash[:],
		Vout:    0,
		Network: pb.Network_REGTEST,
	}, rawTx, []pb.UtxoSwapStatus{pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CREATED}, pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CANCELLED)

	_, err = handler.RollbackInstantUtxoSwap(ctx, cfg, req)
	require.NoError(t, err)

	// Commit tx before checking the result
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	// Verify UtxoSwap is now cancelled
	updatedUtxoSwap, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedUtxoSwap.Status)
}

func TestRollbackInstantUtxoSwap_StatusNotMatching(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{3})
	depositKeyPair := keys.MustGeneratePrivateKeyFromRand(rng)

	const txAmount int64 = 50000
	rawTx, txHash, addrStr := createTestP2TRTx(t, depositKeyPair.Public(), txAmount, btcnetwork.Regtest)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress, err := sessionCtx.Client.DepositAddress.Create().
		SetAddress(addrStr).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetSigningKeyshare(keyshare).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)

	// Create the UTXO
	utxo, err := sessionCtx.Client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txHash[:]).
		SetVout(0).
		SetBlockHeight(50).
		SetAmount(uint64(txAmount)).
		SetPkScript([]byte("test_pk_script")).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	// Create UtxoSwap in COMPLETED status
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetUtxo(utxo).
		SetUtxoValueSats(uint64(txAmount)).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	// Link UtxoSwap to DepositAddress
	_, err = depositAddress.Update().AddUtxoswaps(utxoSwap).Save(ctx)
	require.NoError(t, err)

	// Try to rollback from CREATED — but swap is COMPLETED, so it should not match
	req := generateRollbackInstantRequest(t, ctx, cfg, &pb.UTXO{
		Txid:    txHash[:],
		Vout:    0,
		Network: pb.Network_REGTEST,
	}, rawTx, []pb.UtxoSwapStatus{pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CREATED}, pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CANCELLED)

	resp, err := handler.RollbackInstantUtxoSwap(ctx, cfg, req)
	require.NoError(t, err) // Should not error — just not found
	require.NotNil(t, resp)

	// Verify status is unchanged
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	unchangedSwap, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, unchangedSwap.Status)
}
