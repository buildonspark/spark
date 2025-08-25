//go:build gripmock
// +build gripmock

package handler

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/distributed-lab/gripmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

func TestCreateUtxoSwap_ErrorIfNotCoordinator(t *testing.T) {
	defer func() {
		_ = gripmock.Clear()
	}()

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)

	// Find other operator (non-coordinator)
	var nonCoordinatorID string
	for id := range cfg.SigningOperatorMap {
		if id != cfg.Identifier {
			nonCoordinatorID = id
			break
		}
	}
	if nonCoordinatorID == "" {
		t.Skip("Need at least 2 operators in test config")
	}

	// Change identifier to non-existing operator
	cfg.Identifier = nonCoordinatorID

	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	spendTxBytes := createValidBitcoinTxBytes(ownerIdentityPub)
	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)
	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Fixed,
		1000,
		testSspSignature,
		ownerIdentityPrivKeySecp,
	)
	require.NoError(t, err)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Fixed,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: ownerIdentityPub,
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  spendTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "coordinator")
}

func TestCreateUtxoSwap_InvalidUserSignature(t *testing.T) {
	defer func() {
		_ = gripmock.Clear()
	}()

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create valid signature for a different message (invalid in this case)
	testSspSignature := []byte("valid_ssp_signature")
	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)

	// Create signature for a wrong message
	wrongMessage := []byte("wrong_message_for_signature")
	wrongSignature := ecdsa.Sign(ownerIdentityPrivKeySecp, wrongMessage)
	invalidUserSignature := wrongSignature.Serialize()

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: invalidUserSignature, // Valid signature for a wrong message
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: ownerIdentityPub,
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  createValidBitcoinTxBytes(ownerIdentityPub),
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

func TestCreateUtxoSwap_UtxoNotConfirmed(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	// Set high confirmation threshold
	cfg.BitcoindConfigs["regtest"] = so.BitcoindConfig{
		DepositConfirmationThreshold: 100,
	}

	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 150)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)

	// Create UTXO with insufficient confirmations (150 - 52 + 1 = 99 < 100)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 52)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")
	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)
	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Fixed,
		1000,
		testSspSignature,
		ownerIdentityPrivKeySecp,
	)
	require.NoError(t, err)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Fixed,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: ownerIdentityPub,
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  createValidBitcoinTxBytes(ownerIdentityPub),
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "confirmations")
}

func TestCreateUtxoSwap_InvalidTransferWrongAmount(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")
	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)

	// Use wrong amount in user signature (different from spend tx amount)
	wrongAmount := uint64(9999) // Different from actual spend tx amount
	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		wrongAmount,
		testSspSignature,
		ownerIdentityPrivKeySecp,
	)
	require.NoError(t, err)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: ownerIdentityPub,
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  createValidBitcoinTxBytes(ownerIdentityPub),
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

func TestCreateUtxoSwap_InvalidTransferWrongRecipient(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	_, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)
	wrongRecipientPrivKey, wrongRecipientPub := generateFixedKeyPair(99) // Wrong recipient

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	// Create signature for the wrong recipient
	wrongRecipientPrivKeySecp := secp256k1.PrivKeyFromBytes(wrongRecipientPrivKey)
	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		uint64(utxo.Amount),
		testSspSignature,
		wrongRecipientPrivKeySecp, // use wrong recipient's private key
	)
	require.NoError(t, err)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: wrongRecipientPub, // Wrong recipient
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  createValidBitcoinTxBytes(ownerIdentityPub),
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

func TestCreateUtxoSwap_InvalidTransferWrongNetwork(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")
	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)
	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		uint64(utxo.Amount),
		testSspSignature,
		ownerIdentityPrivKeySecp,
	)
	require.NoError(t, err)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_MAINNET, // Wrong network, should be REGTEST
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: ownerIdentityPub,
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  createValidBitcoinTxBytes(ownerIdentityPub),
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network")
}

func TestCreateUtxoSwap_ErrorNoUtxoSwapCreated(t *testing.T) {
	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create invalid signature
	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")
	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)

	// Create a signature for a wrong message
	wrongMessage := []byte("definitely_wrong_message_that_will_fail_validation")
	wrongSig := ecdsa.Sign(ownerIdentityPrivKeySecp, wrongMessage)
	invalidUserSignature := wrongSig.Serialize()

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: invalidUserSignature, // Invalid signature
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: ownerIdentityPub,
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  createValidBitcoinTxBytes(ownerIdentityPub),
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	// Verify no UtxoSwap exists before the failed attempt
	utxoSwaps, err := sessionCtx.Client.UtxoSwap.Query().All(ctx)
	require.NoError(t, err)
	initialCount := len(utxoSwaps)
	t.Logf("Initial UtxoSwap count: %d", initialCount)

	// This should fail with signature validation error
	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	t.Logf("CreateUtxoSwap returned error: %v", err)
	assert.Error(t, err, "Expected error for invalid signature")
	assert.Contains(t, err.Error(), "signature")

	// Verify no UtxoSwap was created after the error (transaction should be rolled back)
	utxoSwaps, err = sessionCtx.Client.UtxoSwap.Query().All(ctx)
	require.NoError(t, err)
	finalCount := len(utxoSwaps)

	t.Logf("Final UtxoSwap count: %d", finalCount)
	assert.Equal(t, initialCount, finalCount, "No UtxoSwap should be created when there's a validation error")
}

func TestCreateUtxoSwap_SuccessfulCallCreatesUtxoSwap(t *testing.T) {
	defer func() {
		_ = gripmock.Clear()
	}()

	// Setup success stubs
	successStub := map[string]interface{}{
		"UtxoDepositAddress": "bc1ptest_static_deposit_address_for_testing",
	}
	err := gripmock.AddStub("spark_internal.SparkInternalService", "create_utxo_swap", nil, successStub)
	require.NoError(t, err)

	ctx, sessionCtx := setupPgTestContext(t)
	defer sessionCtx.Close()

	cfg := setupTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	ownerIdentityPrivKey, ownerIdentityPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPub, ownerSigningPub)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	spendTxBytes := createValidBitcoinTxBytes(ownerIdentityPub)
	spendTx, err := common.TxFromRawTxBytes(spendTxBytes)
	require.NoError(t, err)

	totalAmount := int64(0)
	for _, txOut := range spendTx.TxOut {
		totalAmount += txOut.Value
	}
	spendTxAmount := uint64(totalAmount)

	onChainTxOut := wire.NewTxOut(int64(utxo.Amount), utxo.PkScript)
	spendTxSighash, err := common.SigHashFromTx(spendTx, 0, onChainTxOut)
	require.NoError(t, err)

	ownerIdentityPrivKeySecp := secp256k1.PrivKeyFromBytes(ownerIdentityPrivKey)

	userSignature, err := createValidUserSignatureForTest(
		utxo.Txid,
		uint32(utxo.Vout),
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		spendTxAmount,
		spendTxSighash,
		ownerIdentityPrivKeySecp,
	)
	require.NoError(t, err)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    uint32(utxo.Vout),
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  spendTxSighash,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID(),
			OwnerIdentityPublicKey:    ownerIdentityPub,
			ReceiverIdentityPublicKey: ownerIdentityPub,
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPub,
			RawTx:                  spendTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	// Verify no UtxoSwap exists before
	utxoSwaps, err := sessionCtx.Client.UtxoSwap.Query().All(ctx)
	require.NoError(t, err)
	initialCount := len(utxoSwaps)
	t.Logf("Initial UtxoSwap count: %d", initialCount)

	resp, err := handler.CreateUtxoSwap(ctx, cfg, createRequest)
	t.Logf("CreateUtxoSwap result - error: %v, response: %v", err, resp)

	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Commit tx before checking the result
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	err = tx.Commit()
	require.NoError(t, err)

	// Verify UtxoSwap was created
	utxoSwaps, err = sessionCtx.Client.UtxoSwap.Query().All(t.Context())
	require.NoError(t, err)
	finalCount := len(utxoSwaps)
	t.Logf("Final UtxoSwap count: %d", finalCount)

	assert.Equal(t, initialCount+1, finalCount, "One UtxoSwap should be created")

	// Verify the created UtxoSwap has correct properties (only if created)
	if finalCount > 0 {
		createdUtxoSwap := utxoSwaps[finalCount-1]
		assert.Equal(t, st.UtxoSwapStatusCreated, createdUtxoSwap.Status)
		t.Logf("Created UtxoSwap: %+v", createdUtxoSwap)
	}

	// Verify deposit address is assigned
	if resp != nil {
		assert.Equal(t, "bc1ptest_static_deposit_address_for_testing", resp.UtxoDepositAddress)
	}
}
