package handler_test

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/walletsetting"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUpdateWalletSetting_CreateNew(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test creating new wallet setting
	privateEnabled := true
	request := &pb.UpdateWalletSettingRequest{
		PrivateEnabled: &privateEnabled,
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)

	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.True(t, resp.WalletSetting.PrivateEnabled)

	// Verify it was saved to database
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	savedSetting, err := database.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, identityPubKey, savedSetting.OwnerIdentityPublicKey)
	assert.True(t, savedSetting.PrivateEnabled)
}

func TestUpdateWalletSetting_UpdateExisting(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	// Create existing wallet setting
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	existingSetting, err := database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(identityPubKey).
		SetPrivateEnabled(false).
		Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, existingSetting)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test updating existing wallet setting
	privateEnabled := true
	request := &pb.UpdateWalletSettingRequest{
		PrivateEnabled: &privateEnabled,
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)

	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.True(t, resp.WalletSetting.PrivateEnabled)

	// Verify it was updated in database
	updatedSetting, err := database.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, identityPubKey, updatedSetting.OwnerIdentityPublicKey)
	assert.True(t, updatedSetting.PrivateEnabled)
	assert.Equal(t, existingSetting.ID, updatedSetting.ID) // Same record
}

func TestUpdateWalletSetting_NoFieldsProvided(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test with no fields provided
	request := &pb.UpdateWalletSettingRequest{
		// PrivateEnabled is nil
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.Error(t, err)
	require.Nil(t, resp)

	grpcErr, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcErr.Code())
	assert.Contains(t, grpcErr.Message(), "at least one field must be provided for update")
}

func TestUpdateWalletSetting_NoSession(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test with no session context
	privateEnabled := true
	request := &pb.UpdateWalletSettingRequest{
		PrivateEnabled: &privateEnabled,
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.Error(t, err)
	require.Nil(t, resp)
}

// createTestContextWithKnobsBypassed creates a test context with knobs that always return true for privacy
func createTestContextWithKnobsBypassed(t *testing.T) (context.Context, *so.Config) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)

	// Create fixed knobs that always enable privacy (bypass knob check)
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100, // 100% rollout = always enabled
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	return ctx, cfg
}

func TestHasReadAccessToWallet_NoWalletSetting(t *testing.T) {
	ctx, cfg := createTestContextWithKnobsBypassed(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key for wallet owner
	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Generate test identity public key for session user (different from owner)
	sessionUserPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(sessionUserPubKey.Serialize()), 9999999999)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test when no wallet setting exists - should return true (default: no privacy, everyone has access)
	hasAccess, err := walletSettingHandler.HasReadAccessToWallet(ctx, walletOwnerPubKey)
	require.NoError(t, err)
	assert.True(t, hasAccess)
}

func TestHasReadAccessToWallet_PrivacyDisabled(t *testing.T) {
	ctx, cfg := createTestContextWithKnobsBypassed(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key for wallet owner
	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Generate test identity public key for session user (different from owner)
	sessionUserPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(sessionUserPubKey.Serialize()), 9999999999)

	// Create wallet setting with privacy disabled
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	_, err = database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(false).
		Save(ctx)
	require.NoError(t, err)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test when privacy is disabled - should return true (everyone has access)
	hasAccess, err := walletSettingHandler.HasReadAccessToWallet(ctx, walletOwnerPubKey)
	require.NoError(t, err)
	assert.True(t, hasAccess)
}

func TestHasReadAccessToWallet_PrivacyEnabled_OwnerAccess(t *testing.T) {
	ctx, cfg := createTestContextWithKnobsBypassed(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key for wallet owner
	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context as the owner
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(walletOwnerPubKey.Serialize()), 9999999999)

	// Create wallet setting with privacy enabled
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	_, err = database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test when privacy is enabled and user is the owner - should return true
	hasAccess, err := walletSettingHandler.HasReadAccessToWallet(ctx, walletOwnerPubKey)
	require.NoError(t, err)
	assert.True(t, hasAccess)
}

func TestHasReadAccessToWallet_PrivacyEnabled_MasterAccess(t *testing.T) {
	ctx, cfg := createTestContextWithKnobsBypassed(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key for wallet owner
	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Generate test identity public key for master
	masterPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Generate test identity public key for session user (different from owner, but matches master)
	sessionUserPubKey := masterPubKey

	// Set up session context as the master
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(sessionUserPubKey.Serialize()), 9999999999)

	// Create wallet setting with privacy enabled and master set
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	_, err = database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		SetMasterIdentityPublicKey(masterPubKey).
		Save(ctx)
	require.NoError(t, err)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test when privacy is enabled and user is the master - should return true
	hasAccess, err := walletSettingHandler.HasReadAccessToWallet(ctx, walletOwnerPubKey)
	require.NoError(t, err)
	assert.True(t, hasAccess)
}

func TestHasReadAccessToWallet_PrivacyEnabled_NoAccess(t *testing.T) {
	ctx, cfg := createTestContextWithKnobsBypassed(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key for wallet owner
	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Generate test identity public key for session user (different from owner and not master)
	sessionUserPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(sessionUserPubKey.Serialize()), 9999999999)

	// Create wallet setting with privacy enabled
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	_, err = database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test when privacy is enabled and user is neither owner nor master - should return false
	hasAccess, err := walletSettingHandler.HasReadAccessToWallet(ctx, walletOwnerPubKey)
	require.NoError(t, err)
	assert.False(t, hasAccess)
}

func TestHasReadAccessToWallet_KnobDisabled(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key for wallet owner
	walletOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Generate test identity public key for session user (different from owner)
	sessionUserPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(sessionUserPubKey.Serialize()), 9999999999)

	// Create fixed knobs that disable privacy (0% rollout)
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 0, // 0% rollout = disabled
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	// Create wallet setting with privacy enabled
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	_, err = database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(walletOwnerPubKey).
		SetPrivateEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test when privacy knob is disabled - should return true (everyone has access)
	hasAccess, err := walletSettingHandler.HasReadAccessToWallet(ctx, walletOwnerPubKey)
	require.NoError(t, err)
	assert.True(t, hasAccess)
}

func TestQueryWalletSetting_Existing(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	masterPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Create existing wallet setting
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	existingSetting, err := client.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(identityPubKey).
		SetPrivateEnabled(true).
		SetMasterIdentityPublicKey(masterPubKey).
		Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, existingSetting)

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)
	resp, err := walletSettingHandler.QueryWalletSetting(ctx, &pb.QueryWalletSettingRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)

	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.True(t, resp.WalletSetting.PrivateEnabled)
	assert.Equal(t, masterPubKey.Serialize(), resp.WalletSetting.MasterIdentityPublicKey)
}

func TestQueryWalletSetting_NotExisting(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)
	resp, err := walletSettingHandler.QueryWalletSetting(ctx, &pb.QueryWalletSettingRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)

	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.False(t, resp.WalletSetting.PrivateEnabled)        // default value from schema
	assert.Nil(t, resp.WalletSetting.MasterIdentityPublicKey) // nil by default

	// Verify it was saved to database
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	savedSetting, err := database.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, identityPubKey, savedSetting.OwnerIdentityPublicKey)
	assert.False(t, savedSetting.PrivateEnabled)        // default value from schema
	assert.Nil(t, savedSetting.MasterIdentityPublicKey) // nil by default
}

func TestUpdateWalletSetting_SetMasterIdentityPublicKey_CreateNew(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	masterPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test creating new wallet setting with master_identity_public_key
	request := &pb.UpdateWalletSettingRequest{
		MasterIdentityPublicKey: &pb.UpdateWalletSettingRequest_SetMasterIdentityPublicKey{
			SetMasterIdentityPublicKey: masterPubKey.Serialize(),
		},
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)
	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.Equal(t, masterPubKey.Serialize(), resp.WalletSetting.MasterIdentityPublicKey)

	// Verify it was saved to database
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	savedSetting, err := database.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, identityPubKey, savedSetting.OwnerIdentityPublicKey)
	assert.NotNil(t, savedSetting.MasterIdentityPublicKey)
	assert.Equal(t, masterPubKey, *savedSetting.MasterIdentityPublicKey)
}

func TestUpdateWalletSetting_SetMasterIdentityPublicKey_UpdateExisting(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	masterPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	// Create existing wallet setting without master key
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	existingSetting, err := database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(identityPubKey).
		SetPrivateEnabled(false).
		Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, existingSetting)
	assert.Nil(t, existingSetting.MasterIdentityPublicKey)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test updating existing wallet setting with master_identity_public_key
	request := &pb.UpdateWalletSettingRequest{
		MasterIdentityPublicKey: &pb.UpdateWalletSettingRequest_SetMasterIdentityPublicKey{
			SetMasterIdentityPublicKey: masterPubKey.Serialize(),
		},
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)

	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.Equal(t, masterPubKey.Serialize(), resp.WalletSetting.MasterIdentityPublicKey)

	// Verify it was updated in database
	updatedSetting, err := database.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, identityPubKey, updatedSetting.OwnerIdentityPublicKey)
	assert.NotNil(t, updatedSetting.MasterIdentityPublicKey)
	assert.Equal(t, masterPubKey, *updatedSetting.MasterIdentityPublicKey)
	assert.Equal(t, existingSetting.ID, updatedSetting.ID) // Same record
}

func TestUpdateWalletSetting_ClearMasterIdentityPublicKey(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	masterPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	// Create existing wallet setting with master key
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	existingSetting, err := database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(identityPubKey).
		SetPrivateEnabled(false).
		SetMasterIdentityPublicKey(masterPubKey).
		Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, existingSetting)
	assert.NotNil(t, existingSetting.MasterIdentityPublicKey)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test clearing master_identity_public_key
	request := &pb.UpdateWalletSettingRequest{
		MasterIdentityPublicKey: &pb.UpdateWalletSettingRequest_ClearMasterIdentityPublicKey{
			ClearMasterIdentityPublicKey: true,
		},
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)

	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.Nil(t, resp.WalletSetting.MasterIdentityPublicKey)

	// Verify it was cleared in database
	updatedSetting, err := database.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, identityPubKey, updatedSetting.OwnerIdentityPublicKey)
	assert.Nil(t, updatedSetting.MasterIdentityPublicKey)
	assert.Equal(t, existingSetting.ID, updatedSetting.ID) // Same record
}

func TestUpdateWalletSetting_UpdateBothFields(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	masterPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	// Create existing wallet setting
	database, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	existingSetting, err := database.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(identityPubKey).
		SetPrivateEnabled(false).
		Save(ctx)
	require.NoError(t, err)
	require.NotNil(t, existingSetting)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test updating both private_enabled and master_identity_public_key
	privateEnabled := true
	request := &pb.UpdateWalletSettingRequest{
		PrivateEnabled: &privateEnabled,
		MasterIdentityPublicKey: &pb.UpdateWalletSettingRequest_SetMasterIdentityPublicKey{
			SetMasterIdentityPublicKey: masterPubKey.Serialize(),
		},
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.WalletSetting)

	assert.Equal(t, identityPubKey.Serialize(), resp.WalletSetting.OwnerIdentityPublicKey)
	assert.True(t, resp.WalletSetting.PrivateEnabled)
	assert.Equal(t, masterPubKey.Serialize(), resp.WalletSetting.MasterIdentityPublicKey)

	// Verify both fields were updated in database
	updatedSetting, err := database.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, identityPubKey, updatedSetting.OwnerIdentityPublicKey)
	assert.True(t, updatedSetting.PrivateEnabled)
	assert.NotNil(t, updatedSetting.MasterIdentityPublicKey)
	assert.Equal(t, masterPubKey, *updatedSetting.MasterIdentityPublicKey)
	assert.Equal(t, existingSetting.ID, updatedSetting.ID) // Same record
}

func TestUpdateWalletSetting_NoFieldsProvided_WithOneof(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// Set up session context
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test with no fields provided (oneof not set)
	request := &pb.UpdateWalletSettingRequest{
		// PrivateEnabled is nil
		// MasterIdentityPublicKey is nil (oneof not set)
	}

	resp, err := walletSettingHandler.UpdateWalletSetting(ctx, request)
	require.Error(t, err)
	require.Nil(t, resp)

	grpcErr, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcErr.Code())
	assert.Contains(t, grpcErr.Message(), "at least one field must be provided for update")
}
