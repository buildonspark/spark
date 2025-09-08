//go:build gripmock
// +build gripmock

package handler

import (
	"encoding/hex"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/distributed-lab/gripmock"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
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

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

	cfg := setUpTestConfigWithRegtestNoAuthz(t)

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
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPrivKey.Public())
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	spendTxBytes := createValidBitcoinTxBytes(t, ownerIdentityPubKey)
	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	userSignature := createValidUserSignatureForTest(
		utxo.Txid,
		utxo.Vout,
		common.Regtest,
		pb.UtxoSwapRequestType_Fixed,
		1000,
		testSspSignature,
		ownerIdentityPrivKey,
	)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Fixed,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: ownerIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPrivKey.Public().Serialize(),
			RawTx:                  spendTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	require.ErrorContains(t, err, "coordinator")
}

func TestCreateUtxoSwap_InvalidUserSignature(t *testing.T) {
	defer func() {
		_ = gripmock.Clear()
	}()

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPrivKey.Public())
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create valid signature for a different message (invalid in this case)
	testSspSignature := []byte("valid_ssp_signature")

	// Create signature for a wrong message
	wrongMessage := []byte("wrong_message_for_signature")
	invalidUserSignature := ecdsa.Sign(ownerIdentityPrivKey.ToBTCEC(), wrongMessage).Serialize()

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: invalidUserSignature, // Valid signature for a wrong message
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: ownerIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPrivKey.Public().Serialize(),
			RawTx:                  createValidBitcoinTxBytes(t, ownerIdentityPubKey),
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	require.ErrorContains(t, err, "signature")
}

func TestCreateUtxoSwap_UtxoNotConfirmed(t *testing.T) {
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()
	rng := rand.NewChaCha8([32]byte{})

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	// Set high confirmation threshold
	cfg.BitcoindConfigs["regtest"] = so.BitcoindConfig{
		DepositConfirmationThreshold: 100,
	}

	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 150)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPubKey := ownerSigningPrivKey.Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)

	// Create UTXO with insufficient confirmations (150 - 52 + 1 = 99 < 100)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 52)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	userSignature := createValidUserSignatureForTest(
		utxo.Txid,
		utxo.Vout,
		common.Regtest,
		pb.UtxoSwapRequestType_Fixed,
		1000,
		testSspSignature,
		ownerSigningPrivKey,
	)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Fixed,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: ownerIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  createValidBitcoinTxBytes(t, ownerIdentityPubKey),
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	require.ErrorContains(t, err, "confirmations")
}

func TestCreateUtxoSwap_InvalidTransferWrongAmount(t *testing.T) {
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	// Use wrong amount in user signature (different from spend tx amount)
	wrongAmount := uint64(9999) // Different from actual spend tx amount
	userSignature := createValidUserSignatureForTest(
		utxo.Txid,
		utxo.Vout,
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		wrongAmount,
		testSspSignature,
		ownerIdentityPrivKey,
	)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: ownerIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  createValidBitcoinTxBytes(t, ownerIdentityPubKey),
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	require.ErrorContains(t, err, "signature")
}

func TestCreateUtxoSwap_InvalidTransferWrongRecipient(t *testing.T) {
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	wrongRecipientPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	// Create signature for the wrong recipient
	userSignature := createValidUserSignatureForTest(
		utxo.Txid,
		utxo.Vout,
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		utxo.Amount,
		testSspSignature,
		wrongRecipientPrivKey,
	)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: wrongRecipientPrivKey.Public().Serialize(), // Wrong recipient
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  createValidBitcoinTxBytes(t, ownerIdentityPubKey),
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	require.Error(t, err, "signature")
}

func TestCreateUtxoSwap_InvalidTransferWrongNetwork(t *testing.T) {
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	userSignature := createValidUserSignatureForTest(
		utxo.Txid,
		utxo.Vout,
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		utxo.Amount,
		testSspSignature,
		ownerIdentityPrivKey,
	)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_MAINNET, // Wrong network, should be REGTEST
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: ownerIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  createValidBitcoinTxBytes(t, ownerIdentityPubKey),
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	_, err = handler.CreateUtxoSwap(ctx, cfg, createRequest)
	require.ErrorContains(t, err, "network")
}

func TestCreateUtxoSwap_ErrorNoUtxoSwapCreated(t *testing.T) {
	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	// Create invalid signature
	testSspSignature, _ := hex.DecodeString("abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")

	// Create a signature for a wrong message
	wrongMessage := []byte("definitely_wrong_message_that_will_fail_validation")
	invalidUserSignature := ecdsa.Sign(ownerIdentityPrivKey.ToBTCEC(), wrongMessage).Serialize()

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  testSspSignature,
		UserSignature: invalidUserSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: ownerIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  createValidBitcoinTxBytes(t, ownerIdentityPubKey),
			SigningNonceCommitment: createTestSigningCommitment(rng),
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
	require.ErrorContains(t, err, "signature")

	// Verify no UtxoSwap was created after the error (transaction should be rolled back)
	utxoSwaps, err = sessionCtx.Client.UtxoSwap.Query().All(ctx)
	require.NoError(t, err)
	finalCount := len(utxoSwaps)

	assert.Equal(t, initialCount, finalCount, "No UtxoSwap should be created when there's a validation error")
}

func TestCreateUtxoSwap_SuccessfulCallCreatesUtxoSwap(t *testing.T) {
	defer func() {
		_ = gripmock.Clear()
	}()

	// Setup success stubs
	successStub := map[string]any{
		"UtxoDepositAddress": "bc1ptest_static_deposit_address_for_testing",
	}
	err := gripmock.AddStub("spark_internal.SparkInternalService", "create_utxo_swap", nil, successStub)
	require.NoError(t, err)

	ctx, sessionCtx := db.SetUpPostgresTestContext(t)
	defer sessionCtx.Close()

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewInternalDepositHandler(cfg)

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPubKey := ownerIdentityPrivKey.Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, ownerIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	spendTxBytes := createValidBitcoinTxBytes(t, ownerIdentityPubKey)
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

	userSignature := createValidUserSignatureForTest(
		utxo.Txid,
		utxo.Vout,
		common.Regtest,
		pb.UtxoSwapRequestType_Refund,
		spendTxAmount,
		spendTxSighash,
		ownerIdentityPrivKey,
	)

	req := &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
			Network: pb.Network_REGTEST,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		SspSignature:  spendTxSighash,
		UserSignature: userSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                testTransferID.String(),
			OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
			ReceiverIdentityPublicKey: ownerIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
		SpendTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerSigningPubKey.Serialize(),
			RawTx:                  spendTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	createRequest, err := CreateCreateSwapForUtxoRequest(cfg, req)
	require.NoError(t, err)

	// Verify no UtxoSwap exists before
	initialCount, err := sessionCtx.Client.UtxoSwap.Query().Count(ctx)
	require.NoError(t, err)

	resp, err := handler.CreateUtxoSwap(ctx, cfg, createRequest)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	// Verify deposit address is assigned
	assert.Equal(t, "bc1ptest_static_deposit_address_for_testing", resp.GetUtxoDepositAddress())

	// Commit tx before checking the result
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Verify UtxoSwap was created
	utxoSwaps, err := sessionCtx.Client.UtxoSwap.Query().All(t.Context())
	require.NoError(t, err)
	require.Len(t, utxoSwaps, initialCount+1, "One UtxoSwap should be created")
	createdUtxoSwap := utxoSwaps[len(utxoSwaps)-1]
	assert.Equal(t, st.UtxoSwapStatusCreated, createdUtxoSwap.Status)

}
