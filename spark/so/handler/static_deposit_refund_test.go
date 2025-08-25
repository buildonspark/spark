//go:build gripmock
// +build gripmock

package handler

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
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
	utxo *ent.Utxo,
	ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub []byte,
) *pb.InitiateStaticDepositUtxoRefundRequest {

	refundTxBytes := createValidBitcoinTxBytes(ownerIdentityPub)
	spendTx, err := common.TxFromRawTxBytes(refundTxBytes)
	if err != nil {
		panic(err)
	}

	// Calculate total amount from spend tx
	totalAmount := int64(0)
	for _, txOut := range spendTx.TxOut {
		totalAmount += txOut.Value
	}

	// Create sighash for user signature
	onChainTxOut := wire.NewTxOut(int64(utxo.Amount), utxo.PkScript)
	spendTxSigHash, err := common.SigHashFromTx(spendTx, 0, onChainTxOut)
	if err != nil {
		panic(err)
	}

	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)
	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		uint64(totalAmount),
		spendTxSigHash,
		ownerIdentityPrivKeySecp,
	)
	if err != nil {
		panic(err)
	}

	return &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  refundTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(),
		},
		UserSignature: userSignature,
	}
}

func TestCreateStaticDepositUtxoRefundWithRollback_Success(t *testing.T) {
	defer func() {
		_ = gripmock.Clear()
	}()

	ctx, sessionCtx := setupPgTestContext(t)

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	ownerIdentityPub := ownerIdentityPrivKey.PubKey().SerializeCompressed()

	ownerSigningPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	ownerSigningPub := ownerSigningPrivKey.PubKey().SerializeCompressed()

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)

	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	successStub := map[string]interface{}{
		"UtxoDepositAddress": depositAddress.Address,
	}
	err = gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "utxo_swap_completed", nil, nil)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err)

	err = gripmock.AddStub("spark_internal.SparkInternalService", "rollback_utxo_swap", nil, nil)
	require.NoError(t, err)

	refundTxBytes := createValidBitcoinTxBytes(ownerIdentityPub)

	spendTx, err := common.TxFromRawTxBytes(refundTxBytes)
	require.NoError(t, err)

	onChainTxOut := wire.NewTxOut(int64(utxo.Amount), utxo.PkScript)
	spendTxSigHash, err := common.SigHashFromTx(spendTx, 0, onChainTxOut)
	require.NoError(t, err)

	totalAmount := int64(0)
	for _, txOut := range spendTx.TxOut {
		totalAmount += txOut.Value
	}

	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		uint64(totalAmount),
		spendTxSigHash,
		ownerIdentityPrivKey,
	)
	require.NoError(t, err)

	req := &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  refundTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(),
		},
		UserSignature: userSignature,
	}

	err = handler.createStaticDepositUtxoRefundWithRollback(ctx, cfg, req)
	assert.NoError(t, err)
}

func TestInitiateStaticDepositUtxoRefund_ErrorIfUtxoNotToStaticDepositAddress(t *testing.T) {
	defer func() {
		_ = gripmock.Clear()
	}()

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)

	// Create non-static deposit address
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	depositAddress, err := sessionCtx.Client.DepositAddress.UpdateOne(depositAddress).SetIsStatic(false).Save(ctx)
	require.NoError(t, err)

	successStub := map[string]interface{}{
		"UtxoDepositAddress": depositAddress.Address,
	}

	err = gripmock.AddStub("spark_internal.SparkInternalService", "create_static_deposit_utxo_refund", nil, successStub)
	require.NoError(t, err)

	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	req := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unable to claim a deposit to a non-static address")
}

func TestInitiateStaticDepositUtxoRefund_UtxoNotConfirmed(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	// Set high confirmation threshold
	cfg.BitcoindConfigs["regtest"] = so.BitcoindConfig{
		DepositConfirmationThreshold: 100,
	}

	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 150)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)

	// Create UTXO with insufficient confirmations (150 - 52 + 1 = 99 < 100)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 52)

	req := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	_, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "confirmations")
}

func TestInitiateStaticDepositUtxoRefund_ErrorIfUtxoSwapAlreadyInProgress(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing UTXO swap with Created status
	_ = createTestUtxoSwap(t, ctx, sessionCtx.Client, utxo, st.UtxoSwapStatusCreated)

	req := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	_, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "utxo swap is already registered")
}

func TestInitiateStaticDepositUtxoRefund_ErrorIfUtxoSwapAlreadyCompletedAsClaim(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing completed UTXO swap with claim type
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount). // Claim type
		SetUserIdentityPublicKey(ownerIdentityPub).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(utxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPub).
		Save(ctx)
	require.NoError(t, err)

	req := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	_, err = handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "utxo swap is already registered")

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

	aggregateFrostStubOutput := map[string]interface{}{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing completed refund swap by the same caller
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetUserIdentityPublicKey(ownerIdentityPub). // Same owner
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(utxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPub).
		Save(ctx)
	require.NoError(t, err)

	req := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	// Should succeed and allow signing again
	resp, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.RefundTxSigningResult)

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
	successStub := map[string]interface{}{
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

	aggregateFrostStubOutput := map[string]interface{}{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create previous failed refund attempts (cancelled)
	previousRefundSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCancelled).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetUserIdentityPublicKey(ownerIdentityPub).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(utxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPub).
		Save(ctx)
	require.NoError(t, err)

	// Create previous failed claim attempt (cancelled)
	previousClaimSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCancelled).
		SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
		SetUserIdentityPublicKey(ownerIdentityPub).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(utxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPub).
		Save(ctx)
	require.NoError(t, err)

	req := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	// Should succeed despite previous failed attempts
	resp, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.RefundTxSigningResult)

	// Commit tx before checking the result
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	err = tx.Commit()
	require.NoError(t, err)

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
	successStub := map[string]interface{}{
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

	aggregateFrostStubOutput := map[string]interface{}{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	testUtxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	req := createMockInitiateStaticDepositUtxoRefundRequest(
		testUtxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	resp, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.RefundTxSigningResult)
	assert.NotEmpty(t, resp.DepositAddress)

	// Commit tx before checking the result
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	err = tx.Commit()
	require.NoError(t, err)

	// Find the specific refund swap created for this UTXO
	createdSwap, err := sessionCtx.Client.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(testUtxo.ID))).
		Where(utxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeRefund)).
		Where(utxoswap.StatusEQ(st.UtxoSwapStatusCompleted)).
		Only(context.Background())
	require.NoError(t, err)
	require.NotNil(t, createdSwap, "Refund UtxoSwap should be created for this UTXO")

	assert.Equal(t, st.UtxoSwapStatusCompleted, createdSwap.Status)
	assert.Equal(t, st.UtxoSwapRequestTypeRefund, createdSwap.RequestType)

	// Verify this is the only refund swap for this UTXO
	refundSwapCount, err := sessionCtx.Client.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(testUtxo.ID))).
		Where(utxoswap.RequestTypeEQ(st.UtxoSwapRequestTypeRefund)).
		Count(context.Background())
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

	aggregateFrostStubOutput := map[string]interface{}{
		"signature": []byte("test_aggregated_signature"),
	}
	err = gripmock.AddStub("frost.FrostService", "aggregate_frost", nil, aggregateFrostStubOutput)
	require.NoError(t, err)

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create existing completed refund swap
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCompleted).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetUserIdentityPublicKey(ownerIdentityPub).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey().Serialize()).
		SetUtxo(utxo).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(ownerIdentityPub).
		Save(ctx)
	require.NoError(t, err)

	// First refund request with one transaction
	req1 := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)

	resp1, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req1)
	assert.NoError(t, err)
	assert.NotNil(t, resp1)
	assert.NotNil(t, resp1.RefundTxSigningResult)

	// Second refund request with different transaction bytes
	req2 := createMockInitiateStaticDepositUtxoRefundRequest(
		utxo, ownerIdentityPrivKey, ownerIdentityPub, ownerSigningPub,
	)
	// Modify the raw tx to make it different
	req2.RefundTxSigningJob.RawTx = append(req2.RefundTxSigningJob.RawTx, 0x00)

	resp2, err := handler.InitiateStaticDepositUtxoRefund(ctx, cfg, req2)
	assert.NoError(t, err)
	assert.NotNil(t, resp2)
	assert.NotNil(t, resp2.RefundTxSigningResult)

	// Both should succeed (different signatures for different transactions)
	assert.NotEqual(t, resp1.RefundTxSigningResult, resp2.RefundTxSigningResult)

	// Verify the original swap still exists with completed status
	updatedSwap, err := sessionCtx.Client.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCompleted, updatedSwap.Status)
	assert.Equal(t, st.UtxoSwapRequestTypeRefund, updatedSwap.RequestType)
}
