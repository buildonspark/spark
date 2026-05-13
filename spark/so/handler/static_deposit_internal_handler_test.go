package handler

import (
	"encoding/hex"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateArchiveStaticDepositAddressStatement(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{42})

	// Generate test keys
	testPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testPubKey := testPrivKey.Public()

	tests := []struct {
		name           string
		ownerPubKey    keys.Public
		network        btcnetwork.Network
		address        string
		expectedErrMsg string
	}{
		{
			name:           "valid inputs - mainnet",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Mainnet,
			address:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedErrMsg: "",
		},
		{
			name:           "valid inputs - regtest",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Regtest,
			address:        "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
			expectedErrMsg: "",
		},
		{
			name:           "valid inputs - testnet",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Testnet,
			address:        "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx",
			expectedErrMsg: "",
		},
		{
			name:           "zero public key",
			ownerPubKey:    keys.Public{},
			network:        btcnetwork.Mainnet,
			address:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedErrMsg: "owner identity public key cannot be zero",
		},
		{
			name:           "unspecified network",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Unspecified,
			address:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedErrMsg: "network cannot be unspecified",
		},
		{
			name:           "empty address",
			ownerPubKey:    testPubKey,
			network:        btcnetwork.Mainnet,
			address:        "",
			expectedErrMsg: "address cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := CreateArchiveStaticDepositAddressStatement(tt.ownerPubKey, tt.network, tt.address)

			if tt.expectedErrMsg != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.expectedErrMsg)
				require.Nil(t, hash)
			} else {
				require.NoError(t, err)
				require.NotNil(t, hash)
				require.Len(t, hash, 32, "hash should be 32 bytes (SHA256)")
			}
		})
	}
}

func TestCreateArchiveStaticDepositAddressStatement_KnownVector(t *testing.T) {
	// Test with a known private key to ensure consistent behavior
	privKeyHex := "3418d19f934d800fed3e364568e2d3a34d6574d7fa9459caea7c790e294651a9"
	privKeyBytes, err := hex.DecodeString(privKeyHex)
	require.NoError(t, err)

	privKey, err := keys.ParsePrivateKey(privKeyBytes)
	require.NoError(t, err)
	pubKey := privKey.Public()

	network := btcnetwork.Mainnet
	address := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"

	hash, err := CreateArchiveStaticDepositAddressStatement(pubKey, network, address)
	require.NoError(t, err)
	require.NotNil(t, hash)
	require.Len(t, hash, 32)

	// This is a regression test - the specific hash value ensures the implementation
	// doesn't change unexpectedly. If this test fails after intentional changes to
	// the hashing algorithm, update this expected value.
	expectedHashHex := "1d07f416c53021d26c32bee33ef5f03ba3b63b2399ea822e34a8d0e4109defb8"
	expectedHash, err := hex.DecodeString(expectedHashHex)
	require.NoError(t, err)

	require.Equal(t, expectedHash, hash, "hash should match known test vector")
	t.Logf("Hash verification successful: %x", hash)
}

func TestSaveUtxoForInstantStaticDeposit_Success(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{1})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, userIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	transferID := uuid.New()
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(utxo.Amount).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transferID).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.DepositAddress.UpdateOneID(depositAddress.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	require.NoError(t, err)

	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(utxo.Txid),
		utxo.Vout,
		btcnetwork.Regtest,
	)
	require.NoError(t, err)
	signature := ecdsa.Sign(cfg.IdentityPrivateKey.ToBTCEC(), messageHash)

	req := &pbinternal.SaveUtxoForInstantStaticDepositRequest{
		OnChainUtxo: &pb.UTXO{
			Network: pb.Network_REGTEST,
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
		},
		UtxoSwapId:           utxoSwap.ID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
		TransferId:           transferID.String(),
	}

	resp, err := handler.SaveUtxoForInstantStaticDeposit(ctx, cfg, req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	savedSwap, err := txClient.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	savedUtxo, err := savedSwap.QueryUtxo().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, utxo.ID, savedUtxo.ID)
}

func TestSaveUtxoForInstantStaticDeposit_ErrorIfSwapIDDoesNotMatchTransferID(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{4})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, userIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	transferID := uuid.New()
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(utxo.Amount).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transferID).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.DepositAddress.UpdateOneID(depositAddress.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	require.NoError(t, err)

	otherSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(utxo.Amount).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(uuid.New()).
		Save(ctx)
	require.NoError(t, err)

	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(utxo.Txid),
		utxo.Vout,
		btcnetwork.Regtest,
	)
	require.NoError(t, err)
	signature := ecdsa.Sign(cfg.IdentityPrivateKey.ToBTCEC(), messageHash)

	req := &pbinternal.SaveUtxoForInstantStaticDepositRequest{
		OnChainUtxo: &pb.UTXO{
			Network: pb.Network_REGTEST,
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
		},
		UtxoSwapId:           otherSwap.ID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
		TransferId:           transferID.String(),
	}

	resp, err := handler.SaveUtxoForInstantStaticDeposit(ctx, cfg, req)
	require.Error(t, err)
	require.Nil(t, resp)
	assert.ErrorContains(t, err, "failed to get utxo swap")
}

func TestSaveUtxoForInstantStaticDeposit_ErrorAddressMismatch(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{2})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	keyshareA := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddressA := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshareA, userIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddressA, 100)

	otherOwnerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherOwnerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshareB := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddressB, err := sessionCtx.Client.DepositAddress.Create().
		SetAddress("bc1ptest_other_deposit_address_for_testing").
		SetOwnerIdentityPubkey(otherOwnerIdentityPubKey).
		SetOwnerSigningPubkey(otherOwnerSigningPubKey).
		SetSigningKeyshare(keyshareB).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)

	transferID := uuid.New()
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(utxo.Amount).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transferID).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.DepositAddress.UpdateOneID(depositAddressB.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	require.NoError(t, err)

	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(utxo.Txid),
		utxo.Vout,
		btcnetwork.Regtest,
	)
	require.NoError(t, err)
	signature := ecdsa.Sign(cfg.IdentityPrivateKey.ToBTCEC(), messageHash)

	req := &pbinternal.SaveUtxoForInstantStaticDepositRequest{
		OnChainUtxo: &pb.UTXO{
			Network: pb.Network_REGTEST,
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
		},
		UtxoSwapId:           utxoSwap.ID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
		TransferId:           transferID.String(),
	}

	resp, err := handler.SaveUtxoForInstantStaticDeposit(ctx, cfg, req)
	require.Error(t, err)
	require.Nil(t, resp)
	assert.ErrorContains(t, err, "does not match swap deposit address")
}

func TestSaveUtxoForInstantStaticDeposit_ErrorAmountMismatch(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{3})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, userIdentityPubKey, ownerSigningPubKey)
	utxo := createTestUtxo(t, ctx, sessionCtx.Client, depositAddress, 100)

	transferID := uuid.New()
	utxoSwap, err := sessionCtx.Client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(5000).
		SetCreditAmountSats(4000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transferID).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.DepositAddress.UpdateOneID(depositAddress.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	require.NoError(t, err)

	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(utxo.Txid),
		utxo.Vout,
		btcnetwork.Regtest,
	)
	require.NoError(t, err)
	signature := ecdsa.Sign(cfg.IdentityPrivateKey.ToBTCEC(), messageHash)

	req := &pbinternal.SaveUtxoForInstantStaticDepositRequest{
		OnChainUtxo: &pb.UTXO{
			Network: pb.Network_REGTEST,
			Txid:    utxo.Txid,
			Vout:    utxo.Vout,
		},
		UtxoSwapId:           utxoSwap.ID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
		TransferId:           transferID.String(),
	}

	resp, err := handler.SaveUtxoForInstantStaticDeposit(ctx, cfg, req)
	require.Error(t, err)
	require.Nil(t, resp)
	assert.ErrorContains(t, err, "does not match swap utxo_value_sats")
}

func TestLinkUtxoSwapTransfer_LinksTransferEdge(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{10})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	transferID := uuid.New()
	transfer, err := txClient.Transfer.Create().
		SetID(transferID).
		SetSenderIdentityPubkey(sspIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetStatus(st.TransferStatusSenderKeyTweaked).
		SetType(st.TransferTypeUtxoSwap).
		SetNetwork(btcnetwork.Regtest).
		SetTotalValue(10000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	utxoSwap, err := txClient.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(10000).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transferID).
		Save(ctx)
	require.NoError(t, err)

	// Verify edge is NOT linked yet
	linkedTransfer, err := utxoSwap.QueryTransfer().Only(ctx)
	assert.True(t, ent.IsNotFound(err))
	assert.Nil(t, linkedTransfer)

	messageHash, err := CreateLinkUtxoSwapTransferStatement(transferID.String())
	require.NoError(t, err)
	signature := ecdsa.Sign(cfg.IdentityPrivateKey.ToBTCEC(), messageHash)

	req := &pbinternal.LinkUtxoSwapTransferRequest{
		TransferId:           transferID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
	}

	resp, err := handler.LinkUtxoSwapTransfer(ctx, cfg, req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify edge IS now linked - re-query from DB
	savedSwap, err := txClient.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	linkedTransfer, err = savedSwap.QueryTransfer().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, transfer.ID, linkedTransfer.ID)
}

func TestLinkUtxoSwapTransfer_InvalidSignature(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{11})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transferID := uuid.New()

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	_, err = txClient.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(10000).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transferID).
		Save(ctx)
	require.NoError(t, err)

	// Sign with a different key (not the coordinator)
	wrongKey := keys.MustGeneratePrivateKeyFromRand(rng)
	messageHash, err := CreateLinkUtxoSwapTransferStatement(transferID.String())
	require.NoError(t, err)
	signature := ecdsa.Sign(wrongKey.ToBTCEC(), messageHash)

	req := &pbinternal.LinkUtxoSwapTransferRequest{
		TransferId:           transferID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
	}

	resp, err := handler.LinkUtxoSwapTransfer(ctx, cfg, req)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorContains(t, err, "unable to verify coordinator signature")
}

func TestLinkUtxoSwapTransfer_AlreadyLinked(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{12})
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	transferID := uuid.New()
	transfer, err := txClient.Transfer.Create().
		SetID(transferID).
		SetSenderIdentityPubkey(sspIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetStatus(st.TransferStatusSenderKeyTweaked).
		SetType(st.TransferTypeUtxoSwap).
		SetNetwork(btcnetwork.Regtest).
		SetTotalValue(10000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	utxoSwap, err := txClient.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(10000).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(cfg.IdentityPublicKey()).
		SetRequestedTransferID(transferID).
		SetTransfer(transfer).
		Save(ctx)
	require.NoError(t, err)

	messageHash, err := CreateLinkUtxoSwapTransferStatement(transferID.String())
	require.NoError(t, err)
	signature := ecdsa.Sign(cfg.IdentityPrivateKey.ToBTCEC(), messageHash)

	req := &pbinternal.LinkUtxoSwapTransferRequest{
		TransferId:           transferID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
	}

	// Should succeed idempotently
	resp, err := handler.LinkUtxoSwapTransfer(ctx, cfg, req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Edge should still be linked
	savedSwap, err := txClient.UtxoSwap.Get(ctx, utxoSwap.ID)
	require.NoError(t, err)
	linkedTransfer, err := savedSwap.QueryTransfer().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, transfer.ID, linkedTransfer.ID)
}

func TestLinkUtxoSwapTransfer_SwapNotFound(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	transferID := uuid.New()
	messageHash, err := CreateLinkUtxoSwapTransferStatement(transferID.String())
	require.NoError(t, err)
	signature := ecdsa.Sign(cfg.IdentityPrivateKey.ToBTCEC(), messageHash)

	req := &pbinternal.LinkUtxoSwapTransferRequest{
		TransferId:           transferID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: cfg.IdentityPublicKey().Serialize(),
	}

	resp, err := handler.LinkUtxoSwapTransfer(ctx, cfg, req)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorContains(t, err, "unable to find utxo swap for transfer")
}

func TestLinkUtxoSwapTransfer_NonSOCoordinator(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositInternalHandler(cfg)

	rng := rand.NewChaCha8([32]byte{13})
	nonSOKey := keys.MustGeneratePrivateKeyFromRand(rng)
	sspIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	transferID := uuid.New()
	_, err = txClient.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetRequestType(st.UtxoSwapRequestTypeInstant).
		SetUtxoValueSats(10000).
		SetCreditAmountSats(9000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(userIdentityPubKey).
		SetCoordinatorIdentityPublicKey(nonSOKey.Public()).
		SetRequestedTransferID(transferID).
		Save(ctx)
	require.NoError(t, err)

	messageHash, err := CreateLinkUtxoSwapTransferStatement(transferID.String())
	require.NoError(t, err)
	signature := ecdsa.Sign(nonSOKey.ToBTCEC(), messageHash)

	req := &pbinternal.LinkUtxoSwapTransferRequest{
		TransferId:           transferID.String(),
		Signature:            signature.Serialize(),
		CoordinatorPublicKey: nonSOKey.Public().Serialize(),
	}

	resp, err := handler.LinkUtxoSwapTransfer(ctx, cfg, req)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorContains(t, err, "coordinator is not a signing operator")
}
