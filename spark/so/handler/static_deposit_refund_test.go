//go:build gripmock
// +build gripmock

package handler

import (
	"io"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
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

func createMockInitiateStaticDepositUtxoRefundRequest(
	t *testing.T,
	rng io.Reader,
	utxo *ent.Utxo,
	ownerIdentityPrivKey keys.Private,
	ownerSigningPubKey keys.Public,
) *pb.InitiateStaticDepositUtxoRefundRequest {
	refundTxBytes := createValidBitcoinTxBytes(t, ownerIdentityPrivKey.Public())
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
		common.Regtest,
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
	defer func() {
		_ = gripmock.Clear()
	}()

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
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

	refundTxBytes := createValidBitcoinTxBytes(t, ownerIdentityPubKey)

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
		common.Regtest,
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
	defer func() {
		_ = gripmock.Clear()
	}()

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
		SetUserIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey.Serialize()).
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
	defer func() {
		_ = gripmock.Clear()
	}()

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

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
		SetUserIdentityPublicKey(ownerIdentityPubKey.Serialize()). // Same owner
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey.Serialize()).
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
	defer func() {
		_ = gripmock.Clear()
	}()

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

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
		SetUserIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		Save(ctx)
	require.NoError(t, err)

	// Create previous failed claim attempt (cancelled)
	previousClaimSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCancelled).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetUserIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		Save(ctx)
	require.NoError(t, err)

	req := createMockInitiateStaticDepositUtxoRefundRequest(t, rng, testUtxo, ownerIdentityPrivKey, ownerSigningPubKey)

	// Should succeed despite previous failed attempts
	resp, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	require.NoError(t, err)
	assert.NotNil(t, resp.GetRefundTxSigningResult())

	// Commit tx before checking the result
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

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
	defer func() {
		_ = gripmock.Clear()
	}()

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

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

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
	defer func() {
		_ = gripmock.Clear()
	}()

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

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

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
		SetUserIdentityPublicKey(ownerIdentityPubKey.Serialize()).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(testUtxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPubKey.Serialize()).
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
	req2.RefundTxSigningJob.RawTx = createValidBitcoinTxBytes(t, differentReceiverPubKey)

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
