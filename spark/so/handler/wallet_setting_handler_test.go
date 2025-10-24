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
func TestIsPrivacyEnabled_NoWalletSetting(t *testing.T) {
	ctx, cfg := createTestContextWithKnobsBypassed(t)
	rng := rand.NewChaCha8([32]byte{})

	// Generate test identity public key
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	walletSettingHandler := handler.NewWalletSettingHandler(cfg)

	// Test when no wallet setting exists - should return false (default)
	isEnabled, err := walletSettingHandler.IsPrivacyEnabled(ctx, identityPubKey)
	require.NoError(t, err)
	assert.False(t, isEnabled)
}

func TestIsPrivacyEnabled_WithWalletSetting(t *testing.T) {
	ctx, cfg := createTestContextWithKnobsBypassed(t)
	rng := rand.NewChaCha8([32]byte{})

	testCases := []struct {
		name           string
		privateEnabled bool
		expectedResult bool
	}{
		{
			name:           "privacy_disabled",
			privateEnabled: false,
			expectedResult: false,
		},
		{
			name:           "privacy_enabled",
			privateEnabled: true,
			expectedResult: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate test identity public key for this test case
			identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

			// Create wallet setting with the test case's privacy setting
			database, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			_, err = database.WalletSetting.
				Create().
				SetOwnerIdentityPublicKey(identityPubKey).
				SetPrivateEnabled(tc.privateEnabled).
				Save(ctx)
			require.NoError(t, err)

			walletSettingHandler := handler.NewWalletSettingHandler(cfg)

			// Test the IsPrivacyEnabled function
			isEnabled, err := walletSettingHandler.IsPrivacyEnabled(ctx, identityPubKey)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedResult, isEnabled)
		})
	}
}
