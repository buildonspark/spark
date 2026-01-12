package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/distributed-lab/gripmock"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
)

func createTestUtxoWithOutpointForDepositAddress(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	depositAddress *ent.DepositAddress,
	blockHeight int64,
	txid []byte,
	vout uint32,
	amount uint64,
	pkScript []byte,
) *ent.Utxo {
	t.Helper()

	utxo, err := client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txid).
		SetVout(vout).
		SetBlockHeight(blockHeight).
		SetAmount(amount).
		SetPkScript(pkScript).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)
	return utxo
}

func createSpendTxBytesSpendingOutpoint(t *testing.T, prevTxid chainhash.Hash, prevVout uint32, receiverPubKey keys.Public, amount int64) []byte {
	t.Helper()

	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  prevTxid,
			Index: prevVout,
		},
		Sequence: wire.MaxTxInSequenceNum,
	})
	tx.AddTxOut(&wire.TxOut{
		Value:    amount,
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return buf.Bytes()
}

func createMockInitiateStaticDepositUtxoRefundRequest(
	t *testing.T,
	rng io.Reader,
	utxo *ent.Utxo,
	ownerIdentityPrivKey keys.Private,
	ownerSigningPubKey keys.Public,
) *pb.InitiateStaticDepositUtxoRefundRequest {
	txidString := hex.EncodeToString(utxo.Txid)

	utxoTxid, err := chainhash.NewHashFromStr(txidString)
	require.NoError(t, err)
	refundTxBytes := createSpendTxBytesSpendingOutpoint(t, *utxoTxid, utxo.Vout, ownerIdentityPrivKey.Public(), int64(utxo.Amount))

	spendTx, err := common.TxFromRawTxBytes(refundTxBytes)
	require.NoError(t, err, "unable to parse refund tx")

	// Calculate total amount from spend tx
	totalAmount := int64(0)
	for _, txOut := range spendTx.TxOut {
		totalAmount += txOut.Value
	}

	// Create sighash for user signature
	onChainTxOut := wire.NewTxOut(int64(utxo.Amount), utxo.PkScript)
	spendTxSigHash, err := common.SigHashFromTx(spendTx, 0, onChainTxOut)
	require.NoError(t, err, "unable to construct sig hash tx")

	userSignature := createValidUserSignatureForTest(
		utxo.Txid,
		utxo.Vout,
		btcnetwork.Regtest,
		pb.UtxoSwapRequestType_Refund,
		uint64(totalAmount),
		spendTxSigHash,
		ownerIdentityPrivKey,
	)

	return &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  refundTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		UserSignature: userSignature,
	}
}

func TestCreateStaticDepositUtxoRefundWithRollback_Success(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)

	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	successStub := map[string]any{
		"UtxoDepositAddress": depositAddress.Address,
	}
	err := gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "utxo_swap_completed", nil, nil)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil)
	require.NoError(t, err)

	txidString := hex.EncodeToString(testUtxo.Txid)
	utxoTxid, err := chainhash.NewHashFromStr(txidString)
	require.NoError(t, err)
	refundTxBytes := createSpendTxBytesSpendingOutpoint(t, *utxoTxid, testUtxo.Vout, ownerIdentityPubKey, int64(testUtxo.Amount))

	spendTx, err := common.TxFromRawTxBytes(refundTxBytes)
	require.NoError(t, err)

	onChainTxOut := wire.NewTxOut(int64(testUtxo.Amount), testUtxo.PkScript)
	spendTxSigHash, err := common.SigHashFromTx(spendTx, 0, onChainTxOut)
	require.NoError(t, err)

	totalAmount := int64(0)
	for _, txOut := range spendTx.TxOut {
		totalAmount += txOut.Value
	}

	userSignature := createValidUserSignatureForTest(
		testUtxo.Txid,
		testUtxo.Vout,
		btcnetwork.Regtest,
		pb.UtxoSwapRequestType_Refund,
		uint64(totalAmount),
		spendTxSigHash,
		ownerIdentityPrivKey,
	)

	req := &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    testUtxo.Txid,
			Vout:    testUtxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  refundTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		UserSignature: userSignature,
	}

	err = handler.createStaticDepositUtxoRefundWithRollback(ctx, cfg, req)
	require.NoError(t, err)
}

func TestInitiateStaticDepositUtxoRefund_ErrorIfUtxoNotToStaticDepositAddress(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)

	// Create non-static deposit address
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	depositAddress, err := sessionCtx.Client.DepositAddress.UpdateOne(depositAddress).SetIsStatic(false).Save(ctx)
	require.NoError(t, err)

	successStub := map[string]any{
		"UtxoDepositAddress": depositAddress.Address,
	}

	err = gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub)
	require.NoError(t, err)

	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	require.ErrorContains(t, err, "unable to claim a deposit to a non-static address")
}

func TestInitiateStaticDepositUtxoRefund_UtxoNotConfirmed(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	// Set high confirmation threshold
	cfg.BitcoindConfigs["regtest"] = so.BitcoindConfig{
		DepositConfirmationThreshold: 100,
	}

	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 150)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPrivKey.Public(), ownerSigningPubKey)

	// Create UTXO with insufficient confirmations (150 - 52 + 1 = 99 < 100)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 52)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	_, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	require.ErrorContains(t, err, "confirmations")
}

func TestInitiateStaticDepositUtxoRefund_ErrorIfUtxoSwapAlreadyInProgress(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing UTXO swap with Created status
	_ = createTestUtxoSwap(t, ctx, rng, sessionCtx.Client, testUtxo, st.UtxoSwapStatusCreated)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	_, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.ErrorContains(t, err, "utxo swap is already registered")
}

func TestInitiateStaticDepositUtxoRefund_ErrorIfUtxoSwapAlreadyCompletedAsClaim(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing completed UTXO swap with claim type
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount). // Claim type
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		Save(ctx)
	require.NoError(t, err)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	require.ErrorContains(t, err, "utxo swap is already registered")

	// Verify the completed claim swap still exists
	updatedSwap, err := sessionCtx.Client.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updatedSwap.Status)
	assert.Equal(t, st.UtxoSwapRequestTypeFixedAmount, updatedSwap.RequestType)
}

func TestInitiateStaticDepositUtxoRefund_CanRefundAgainIfAlreadyRefinedBySameCaller(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	// Mock successful signing
	err := gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err)

	aggregateFrostStubOutput := map[string]any{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing completed refund swap by the same caller
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetUserIdentityPublicKey(ownerIdentityPubKey). // Same owner
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		Save(ctx)
	require.NoError(t, err)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	// Should succeed and allow signing again
	resp, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	require.NoError(t, err)
	assert.NotNil(t, resp.GetRefundTxSigningResult())

	// Verify the original completed refund swap still exists
	updatedSwap, err := sessionCtx.Client.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updatedSwap.Status)
	assert.Equal(t, st.UtxoSwapRequestTypeRefund, updatedSwap.RequestType)
}

func TestInitiateStaticDepositUtxoRefund_CanRefundEvenWithPreviousFailedAttempts(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	// Mock successful refund creation
	successStub := map[string]any{
		"UtxoDepositAddress": "bc1ptest_static_deposit_address_for_testing",
	}
	err := gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "utxo_swap_completed", nil, nil)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err)

	aggregateFrostStubOutput := map[string]any{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create previous failed refund attempts (cancelled)
	previousRefundSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCancelled).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		Save(ctx)
	require.NoError(t, err)

	// Create previous failed claim attempt (cancelled)
	previousClaimSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCancelled).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		Save(ctx)
	require.NoError(t, err)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	// Should succeed despite previous failed attempts
	resp, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	require.NoError(t, err)
	assert.NotNil(t, resp.GetRefundTxSigningResult())

	// Commit tx before checking the result
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	// Verify previous failed swaps still exist with cancelled status in separate context
	updatedRefundSwap, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), previousRefundSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedRefundSwap.Status)

	updatedClaimSwap, err := sessionCtx.Client.UtxoSwap.Get(t.Context(), previousClaimSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, updatedClaimSwap.Status)

	// Verify new UtxoSwap was created
	allSwaps, err := sessionCtx.Client.UtxoSwap.Query().All(t.Context())
	require.NoError(t, err)
	assert.Greater(t, len(allSwaps), 2, "New UtxoSwap should be created despite previous failed attempts")
}

func TestInitiateStaticDepositUtxoRefund_SuccessfulRefundCreatesCompletedUtxoSwap(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	// Mock successful refund creation
	successStub := map[string]any{
		"UtxoDepositAddress": "bc1ptest_static_deposit_address_for_testing",
	}
	err := gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "utxo_swap_completed", nil, nil)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("frost.FrostService", "sign_frost", nil, nil)
	require.NoError(t, err)

	aggregateFrostStubOutput := map[string]any{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	resp, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.RefundTxSigningResult)
	assert.NotEmpty(t, resp.DepositAddress)

	// Commit tx before checking the result
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entTx.Commit())

	// Find the specific refund swap created for this UTXO
	createdSwap, err := sessionCtx.Client.UtxoSwap.Query().
		Where(
			utxoswap.HasUtxoWith(utxo.IDEQ(testUtxo.ID)),
			utxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeRefund),
			utxoswap.StatusEQ(st.UtxoSwapStatusCompleted),
		).
		Only(t.Context())
	require.NoError(t, err)
	require.NotNil(t, createdSwap, "Refund UtxoSwap should be created for this UTXO")

	assert.Equal(t, st.UtxoSwapStatusCompleted, createdSwap.Status)
	assert.Equal(t, st.UtxoSwapRequestTypeRefund, createdSwap.RequestType)

	// Verify this is the only refund swap for this UTXO
	refundSwapCount, err := sessionCtx.Client.UtxoSwap.Query().
		Where(
			utxoswap.HasUtxoWith(utxo.IDEQ(testUtxo.ID)),
			utxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeRefund),
		).
		Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, refundSwapCount, "Should have exactly one refund swap for this UTXO")
}

func TestInitiateStaticDepositUtxoRefund_CanSignDifferentRefundTxMultipleTimes(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	// Mock successful signing
	err := gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err)

	aggregateFrostStubOutput := map[string]any{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing completed refund swap
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetUserIdentityPublicKey(ownerIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey).
		Save(ctx)
	require.NoError(t, err)

	// First refund request with one transaction
	req1 := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	resp1, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req1)
	require.NoError(t, err)
	assert.NotNil(t, resp1.GetRefundTxSigningResult())

	// Second refund request with different transaction - use different receiver PubKey key
	differentReceiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	req2 := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	// Replace the transaction with one that has different receiver
	txidString := hex.EncodeToString(testUtxo.Txid)
	utxoTxid, err := chainhash.NewHashFromStr(txidString)
	require.NoError(t, err)
	// Replace the transaction with one that has different receiver (but still spends the same UTXO)
	req2.RefundTxSigningJob.RawTx = createSpendTxBytesSpendingOutpoint(t, *utxoTxid, testUtxo.Vout, differentReceiverPubKey, int64(testUtxo.Amount))

	resp2, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req2)
	require.NoError(t, err)
	assert.NotNil(t, resp2.GetRefundTxSigningResult())

	spendTx1, err := common.TxFromRawTxBytes(req1.RefundTxSigningJob.RawTx)
	require.NoError(t, err)
	spendTx2, err := common.TxFromRawTxBytes(req2.RefundTxSigningJob.RawTx)
	require.NoError(t, err)

	// Verify we're signing different transactions
	assert.NotEqual(t, spendTx1.TxHash(), spendTx2.TxHash())

	// Both responses should succeed - the test verifies we can sign different refund transactions multiple times
	// The different transaction hashes prove we're processing different transactions correctly
	assert.NotEmpty(t, resp1.GetRefundTxSigningResult().GetPublicKeys())
	assert.NotEmpty(t, resp2.GetRefundTxSigningResult().GetPublicKeys())
	assert.NotEmpty(t, resp1.GetRefundTxSigningResult().GetSigningNonceCommitments())
	assert.NotEmpty(t, resp2.GetRefundTxSigningResult().GetSigningNonceCommitments())

	// Verify the original swap still exists with completed status
	updatedSwap, err := sessionCtx.Client.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updatedSwap.Status)
	assert.Equal(t, st.UtxoSwapRequestTypeRefund, updatedSwap.RequestType)
}

func TestInitiateStaticDepositUtxoRefund_RejectsRefundTxSpendingDifferentOutpoint(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)

	verifyingKey := keyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	depositPkScript, err := common.P2TRScriptFromPubKey(verifyingKey)
	require.NoError(t, err)

	const (
		utxoAmountSats = uint64(10_000)
		spendAmount    = int64(9_000)
	)

	sharedTxID := st.NewRandomTxIDForTesting(t).Bytes()
	utxoA := createTestUtxoWithOutpointForDepositAddress(t, ctx, sessionCtx.Client, depositAddress, 100, sharedTxID, 0, utxoAmountSats, depositPkScript)
	utxoB := createTestUtxoWithOutpointForDepositAddress(t, ctx, sessionCtx.Client, depositAddress, 100, sharedTxID, 1, utxoAmountSats, depositPkScript)

	// Simulate a realistic malicious scenario: utxoB is already locked by an in-progress swap/claim,
	// but the caller asks to refund utxoA while providing a tx that actually spends utxoB.
	_ = createTestUtxoSwap(t, ctx, rng, sessionCtx.Client, utxoB, st.UtxoSwapStatusCreated)

	txidString := hex.EncodeToString(utxoB.Txid)
	utxoBTxid, err := chainhash.NewHashFromStr(txidString)
	require.NoError(t, err)
	refundUtxoBTxBytes := createSpendTxBytesSpendingOutpoint(t, *utxoBTxid, utxoB.Vout, ownerIdentityPubKey, spendAmount)

	spendTxSigHash, totalAmount, err := GetTxSigningInfo(ctx, utxoA, refundUtxoBTxBytes)
	require.NoError(t, err)

	userSignatureToRefundUtxoA := createValidUserSignatureForTest(
		utxoA.Txid,
		utxoA.Vout,
		btcnetwork.Regtest,
		pb.UtxoSwapRequestType_Refund,
		totalAmount,
		spendTxSigHash,
		ownerIdentityPrivKey,
	)

	successStub := map[string]any{"UtxoDepositAddress": depositAddress.Address}
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub))
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput))
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput))
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "utxo_swap_completed", nil, nil))

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxoA.Txid,
			Vout:    utxoA.Vout,
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  refundUtxoBTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		UserSignature: userSignatureToRefundUtxoA,
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "unexpected refund transaction structure")
}

func TestInitiateStaticDepositUtxoRefund_RejectsRefundTxWithMultipleInputs(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)

	verifyingKey := keyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	depositPkScript, err := common.P2TRScriptFromPubKey(verifyingKey)
	require.NoError(t, err)

	const utxoAmountSats = uint64(10_000)

	sharedTxID := st.NewRandomTxIDForTesting(t).Bytes()
	utxoA := createTestUtxoWithOutpointForDepositAddress(t, ctx, sessionCtx.Client, depositAddress, 100, sharedTxID, 0, utxoAmountSats, depositPkScript)
	utxoB := createTestUtxoWithOutpointForDepositAddress(t, ctx, sessionCtx.Client, depositAddress, 100, sharedTxID, 1, utxoAmountSats, depositPkScript)

	txidString := hex.EncodeToString(utxoA.Txid)
	utxoATxid, err := chainhash.NewHashFromStr(txidString)
	require.NoError(t, err)

	// Create malicious transaction with MULTIPLE inputs (trying to spend both UTXO A and UTXO B)
	p2trScript, err := common.P2TRScriptFromPubKey(ownerIdentityPubKey)
	require.NoError(t, err)

	maliciousTx := wire.NewMsgTx(3)
	// Add input for UTXO A (the one being requested for refund)
	maliciousTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *utxoATxid,
			Index: utxoA.Vout,
		},
		Sequence: wire.MaxTxInSequenceNum,
	})
	// Add SECOND input for UTXO B (attack: trying to drain additional UTXO)
	maliciousTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *utxoATxid,
			Index: utxoB.Vout,
		},
		Sequence: wire.MaxTxInSequenceNum,
	})
	maliciousTx.AddTxOut(&wire.TxOut{
		Value:    int64(utxoAmountSats * 2), // Trying to claim both UTXOs
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	require.NoError(t, maliciousTx.Serialize(&buf))
	maliciousTxBytes := buf.Bytes()

	spendTxSigHash, totalAmount, err := GetTxSigningInfo(ctx, utxoA, maliciousTxBytes)
	require.NoError(t, err)

	userSignature := createValidUserSignatureForTest(
		utxoA.Txid,
		utxoA.Vout,
		btcnetwork.Regtest,
		pb.UtxoSwapRequestType_Refund,
		totalAmount,
		spendTxSigHash,
		ownerIdentityPrivKey,
	)

	successStub := map[string]any{"UtxoDepositAddress": depositAddress.Address}
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub))

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxoA.Txid,
			Vout:    utxoA.Vout,
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  maliciousTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		UserSignature: userSignature,
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "unexpected refund transaction structure")
}

func TestInitiateStaticDepositUtxoRefund_RejectsRefundTxWithWrongSequence(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	txidString := hex.EncodeToString(testUtxo.Txid)
	utxoTxid, err := chainhash.NewHashFromStr(txidString)
	require.NoError(t, err)

	// Create transaction with WRONG SEQUENCE NUMBER (not MaxTxInSequenceNum)
	p2trScript, err := common.P2TRScriptFromPubKey(ownerIdentityPubKey)
	require.NoError(t, err)

	wrongSequenceTx := wire.NewMsgTx(3)
	wrongSequenceTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *utxoTxid,
			Index: testUtxo.Vout,
		},
		Sequence: 100, // WRONG: Should be MaxTxInSequenceNum (0xFFFFFFFF)
	})
	wrongSequenceTx.AddTxOut(&wire.TxOut{
		Value:    int64(testUtxo.Amount),
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	require.NoError(t, wrongSequenceTx.Serialize(&buf))
	wrongSequenceTxBytes := buf.Bytes()

	spendTxSigHash, totalAmount, err := GetTxSigningInfo(ctx, testUtxo, wrongSequenceTxBytes)
	require.NoError(t, err)

	userSignature := createValidUserSignatureForTest(
		testUtxo.Txid,
		testUtxo.Vout,
		btcnetwork.Regtest,
		pb.UtxoSwapRequestType_Refund,
		totalAmount,
		spendTxSigHash,
		ownerIdentityPrivKey,
	)

	successStub := map[string]any{"UtxoDepositAddress": depositAddress.Address}
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub))

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    testUtxo.Txid,
			Vout:    testUtxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  wrongSequenceTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		UserSignature: userSignature,
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "unexpected refund transaction structure")
}

func TestInitiateStaticDepositUtxoRefund_RejectsRefundTxWithZeroInputs(t *testing.T) {
	sparktesting.RequireGripMock(t)
	defer func() { _ = gripmock.Clear() }()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create transaction with ZERO INPUTS
	p2trScript, err := common.P2TRScriptFromPubKey(ownerIdentityPubKey)
	require.NoError(t, err)

	zeroInputsTx := wire.NewMsgTx(3)
	// Don't add any inputs
	zeroInputsTx.AddTxOut(&wire.TxOut{
		Value:    int64(testUtxo.Amount),
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	require.NoError(t, zeroInputsTx.Serialize(&buf))
	zeroInputsTxBytes := buf.Bytes()

	// Note: We can't call GetTxSigningInfo because a transaction with zero inputs
	// fails to parse. This is expected - the validation will catch it at parse time.
	// We'll provide dummy values for the signature.
	dummySigHash := make([]byte, 32)
	userSignature := createValidUserSignatureForTest(
		testUtxo.Txid,
		testUtxo.Vout,
		btcnetwork.Regtest,
		pb.UtxoSwapRequestType_Refund,
		testUtxo.Amount,
		dummySigHash,
		ownerIdentityPrivKey,
	)

	successStub := map[string]any{"UtxoDepositAddress": depositAddress.Address}
	require.NoError(t, gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub))

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    testUtxo.Txid,
			Vout:    testUtxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  zeroInputsTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		UserSignature: userSignature,
	})

	require.Error(t, err)
	// Zero-input transactions fail at parse time with a more specific error
	require.ErrorContains(t, err, "failed to parse")
}
