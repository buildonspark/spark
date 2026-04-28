package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/distributed-lab/gripmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setUpTestConfigWithThreshold returns a regtest test config with a custom
// DepositConfirmationThreshold. Use this when a test needs the network
// default threshold to differ from 1.
func setUpTestConfigWithThreshold(t *testing.T, threshold uint) *so.Config {
	cfg := sparktesting.TestConfig(t)
	cfg.SupportedNetworks = []btcnetwork.Network{btcnetwork.Regtest}
	cfg.BitcoindConfigs = map[string]so.BitcoindConfig{
		"regtest": {DepositConfirmationThreshold: threshold},
	}
	return cfg
}

// --- RollbackUtxoSwap threshold tests ---

// TestRollbackUtxoSwap_HonorsRequestThreshold verifies the request-supplied
// confirmation_threshold is used when re-verifying the UTXO during rollback.
// Regression test for the production bug where 1-conf instant deposits had
// their rollback rejected because the receiver fell back to threshold=3.
func TestRollbackUtxoSwap_HonorsRequestThreshold(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil))

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	// Network default = 3 confs; the request will override with 1.
	cfg := setUpTestConfigWithThreshold(t, 3)
	handler := NewInternalDepositHandler(cfg)

	// 1 confirmation: BlockHeight tip = 100, UTXO mined at 100.
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
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	one := uint32(1)
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, &one)
	require.NoError(t, err)
	require.NotNil(t, rollbackRequest.ConfirmationThreshold)
	assert.Equal(t, uint32(1), *rollbackRequest.ConfirmationThreshold)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.NoError(t, err)

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	updated, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updated.Status)
}

// TestRollbackUtxoSwap_NilThresholdUsesNetworkDefault is the negative control
// for TestRollbackUtxoSwap_HonorsRequestThreshold: when no threshold is on the
// request, the receiver falls back to the network default and rejects a UTXO
// that doesn't meet that default.
func TestRollbackUtxoSwap_NilThresholdUsesNetworkDefault(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithThreshold(t, 3)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{1})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, rollbackRequest.ConfirmationThreshold)

	_, err = handler.RollbackUtxoSwap(ctx, cfg, rollbackRequest)
	require.ErrorContains(t, err, "doesn't have enough confirmations")
}

// --- UtxoSwapCompleted threshold tests ---

// notNotEnoughConfsError asserts an error (if any) is NOT the
// "doesn't have enough confirmations" error this fix targets. We use this
// rather than require.NoError because UtxoSwapCompleted does additional work
// after the threshold check (transfer linkage, status updates) that is
// orthogonal to this bug. Tests that aren't gripmock-aware can't exercise
// those steps end-to-end without the cross-SO mock.
func notNotEnoughConfsError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	assert.NotContains(t, err.Error(), "doesn't have enough confirmations",
		"threshold-check unexpectedly rejected this request")
}

// TestUtxoSwapCompleted_HonorsRequestThreshold_FixedAmount is the core
// regression test for the production bug. A 1-conf instant deposit creates a
// FIXED_AMOUNT swap on the SO; the cross-SO complete fan-out must use the
// originating threshold, not the network default. We assert specifically that
// the conf-check does NOT reject the request — downstream completion steps
// require a linked transfer and aren't exercised here.
func TestUtxoSwapCompleted_HonorsRequestThreshold_FixedAmount(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithThreshold(t, 3)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{2})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	_, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	one := uint32(1)
	completedRequest, err := CreateCompleteSwapForUtxoRequest(cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, &one)
	require.NoError(t, err)
	require.NotNil(t, completedRequest.ConfirmationThreshold)
	assert.Equal(t, uint32(1), *completedRequest.ConfirmationThreshold)

	_, err = handler.UtxoSwapCompleted(ctx, cfg, completedRequest)
	notNotEnoughConfsError(t, err)
}

// TestUtxoSwapCompleted_FallsBackToInstantRequestType preserves the
// behavior introduced by PR #5844: when the request omits the threshold but
// the swap row is INSTANT, the handler still uses threshold=1.
func TestUtxoSwapCompleted_FallsBackToInstantRequestType(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithThreshold(t, 3)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{3})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	_, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	completedRequest, err := CreateCompleteSwapForUtxoRequest(cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, completedRequest.ConfirmationThreshold)

	_, err = handler.UtxoSwapCompleted(ctx, cfg, completedRequest)
	notNotEnoughConfsError(t, err)
}

// TestUtxoSwapCompleted_DefaultRejectsLowConfFixedAmount confirms we have
// not weakened the default-threshold path: a FIXED_AMOUNT swap with neither
// a request threshold nor an INSTANT request_type is still rejected when the
// network default isn't met.
func TestUtxoSwapCompleted_DefaultRejectsLowConfFixedAmount(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithThreshold(t, 3)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{4})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	_, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	completedRequest, err := CreateCompleteSwapForUtxoRequest(cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	_, err = handler.UtxoSwapCompleted(ctx, cfg, completedRequest)
	require.ErrorContains(t, err, "doesn't have enough confirmations")
}

// --- Constructor tests ---

func TestCreateCompleteSwapForUtxoRequest_SetsThreshold(t *testing.T) {
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	utxo := &pb.UTXO{
		Txid:    make([]byte, 32),
		Vout:    0,
		Network: pb.Network_REGTEST,
	}

	one := uint32(1)
	withThreshold, err := CreateCompleteSwapForUtxoRequest(cfg, utxo, &one)
	require.NoError(t, err)
	require.NotNil(t, withThreshold.ConfirmationThreshold)
	assert.Equal(t, uint32(1), *withThreshold.ConfirmationThreshold)

	withoutThreshold, err := CreateCompleteSwapForUtxoRequest(cfg, utxo, nil)
	require.NoError(t, err)
	assert.Nil(t, withoutThreshold.ConfirmationThreshold)
}

func TestGenerateRollbackStaticDepositUtxoSwapForUtxoRequest_SetsThreshold(t *testing.T) {
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	utxo := &pb.UTXO{
		Txid:    make([]byte, 32),
		Vout:    0,
		Network: pb.Network_REGTEST,
	}

	one := uint32(1)
	withThreshold, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(t.Context(), cfg, utxo, &one)
	require.NoError(t, err)
	require.NotNil(t, withThreshold.ConfirmationThreshold)
	assert.Equal(t, uint32(1), *withThreshold.ConfirmationThreshold)

	withoutThreshold, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(t.Context(), cfg, utxo, nil)
	require.NoError(t, err)
	assert.Nil(t, withoutThreshold.ConfirmationThreshold)
}

// --- Gossip propagation test ---

// TestHandleRollbackUtxoSwapGossipMessage_PropagatesThreshold ensures the
// gossip handler forwards the threshold from the gossip message to the
// internal RollbackUtxoSwap call. With a network default of 3 and tip-conf=1,
// the handler must use the gossip-supplied threshold=1 to succeed.
func TestHandleRollbackUtxoSwapGossipMessage_PropagatesThreshold(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil))

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithThreshold(t, 3)
	gossipHandler := NewGossipHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{5})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	// Reuse the request constructor to get a valid signature, then mirror
	// the relevant fields onto the gossip message.
	one := uint32(1)
	signed, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    utxo.Txid,
		Vout:    utxo.Vout,
		Network: pb.Network_REGTEST,
	}, &one)
	require.NoError(t, err)

	gossipMsg := &pbgossip.GossipMessageRollbackUtxoSwap{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		Signature:             signed.Signature,
		CoordinatorPublicKey:  signed.CoordinatorPublicKey,
		ConfirmationThreshold: &one,
	}

	require.NoError(t, gossipHandler.handleRollbackUtxoSwapGossipMessage(ctx, gossipMsg))

	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	updated, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updated.Status)

	// Negative control: nil threshold falls back to network default and
	// rejects. Covered by TestRollbackUtxoSwap_NilThresholdUsesNetworkDefault;
	// not duplicated here because createTestUtxo's deterministic txid
	// prevents a second UTXO row in the same DB.
}

// Compile-time assertion that the new proto field is accessible on the Go
// types we expect. Catches accidental proto regen drift during review.
var (
	_ = (*pbinternal.RollbackUtxoSwapRequest)(nil).GetConfirmationThreshold
	_ = (*pbinternal.UtxoSwapCompletedRequest)(nil).GetConfirmationThreshold
	_ = (*pbgossip.GossipMessageRollbackUtxoSwap)(nil).GetConfirmationThreshold
)
