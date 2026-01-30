package tokens

import (
	"context"
	"math/big"
	"testing"
	"time"

	mathrand "math/rand/v2"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	freezeTestRng       = mathrand.NewChaCha8([32]byte{0xFE, 0xED})
	freezeTestIssuerKey = keys.MustGeneratePrivateKeyFromRand(freezeTestRng)
	freezeTestOwnerKey  = keys.MustGeneratePrivateKeyFromRand(freezeTestRng)
	freezeTestWrongKey  = keys.MustGeneratePrivateKeyFromRand(freezeTestRng)
)

func createFreezeTestTokenCreate(t *testing.T, ctx context.Context, client *ent.Client, cfg *so.Config, isFreezable bool) *ent.TokenCreate {
	t.Helper()
	creationEntityPubKey := cfg.IdentityPublicKey()
	createInput := &tokenpb.TokenCreateInput{
		TokenName:               testTokenName,
		TokenTicker:             testTokenTicker,
		Decimals:                testTokenDecimals,
		MaxSupply:               testTokenMaxSupplyBytes,
		IsFreezable:             isFreezable,
		IssuerPublicKey:         freezeTestIssuerKey.Public().Serialize(),
		CreationEntityPublicKey: creationEntityPubKey.Serialize(),
	}

	metadata, err := common.NewTokenMetadataFromCreateInput(createInput, sparkpb.Network_REGTEST)
	require.NoError(t, err)
	tokenIdentifier, err := metadata.ComputeTokenIdentifier()
	require.NoError(t, err)

	tokenCreate, err := client.TokenCreate.Create().
		SetIssuerPublicKey(freezeTestIssuerKey.Public()).
		SetTokenName(testTokenName).
		SetTokenTicker(testTokenTicker).
		SetDecimals(testTokenDecimals).
		SetMaxSupply(testTokenMaxSupplyBytes).
		SetIsFreezable(isFreezable).
		SetCreationEntityPublicKey(creationEntityPubKey).
		SetNetwork(btcnetwork.Regtest).
		SetTokenIdentifier(tokenIdentifier).
		Save(ctx)
	require.NoError(t, err)

	return tokenCreate
}

func createFreezeTestRequest(t *testing.T, cfg *so.Config, tokenCreate *ent.TokenCreate, shouldUnfreeze bool) *tokenpb.FreezeTokensRequest {
	t.Helper()
	return createFreezeTestRequestWithKey(t, cfg, tokenCreate, shouldUnfreeze, freezeTestIssuerKey)
}

func createFreezeTestRequestWithKey(t *testing.T, cfg *so.Config, tokenCreate *ent.TokenCreate, shouldUnfreeze bool, signingKey keys.Private) *tokenpb.FreezeTokensRequest {
	t.Helper()
	timestamp := uint64(time.Now().UnixMilli())
	return createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, shouldUnfreeze, signingKey, timestamp)
}

func createFreezeTestRequestWithTimestamp(t *testing.T, cfg *so.Config, tokenCreate *ent.TokenCreate, shouldUnfreeze bool, signingKey keys.Private, timestamp uint64) *tokenpb.FreezeTokensRequest {
	t.Helper()

	payload := &tokenpb.FreezeTokensPayload{
		Version:                   1,
		OwnerPublicKey:            freezeTestOwnerKey.Public().Serialize(),
		TokenIdentifier:           tokenCreate.TokenIdentifier,
		ShouldUnfreeze:            shouldUnfreeze,
		IssuerProvidedTimestamp:   timestamp,
		OperatorIdentityPublicKey: cfg.IdentityPublicKey().Serialize(),
	}

	payloadHash, err := utils.HashFreezeTokensPayload(payload)
	require.NoError(t, err)

	signature := ecdsa.Sign(signingKey.ToBTCEC(), payloadHash)

	return &tokenpb.FreezeTokensRequest{
		FreezeTokensPayload: payload,
		IssuerSignature:     signature.Serialize(),
	}
}

func TestFreezeTokens_SuccessWhenFreezable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)
	req := createFreezeTestRequest(t, cfg, tokenCreate, false)

	resp, err := handler.FreezeTokens(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.ImpactedTokenOutputs)
	assert.Equal(t, big.NewInt(0).Bytes(), resp.ImpactedTokenAmount)
}

func TestFreezeTokens_FailsWhenNotFreezable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, false)
	req := createFreezeTestRequest(t, cfg, tokenCreate, false)

	resp, err := handler.FreezeTokens(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), tokens.ErrTokenNotFreezable)
}

func TestFreezeTokens_IdempotentWhenAlreadyFrozen(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)

	req1 := createFreezeTestRequest(t, cfg, tokenCreate, false)
	resp1, err := handler.FreezeTokens(ctx, req1)
	require.NoError(t, err)
	require.NotNil(t, resp1)

	// Freezing again should succeed (idempotent)
	req2 := createFreezeTestRequest(t, cfg, tokenCreate, false)
	resp2, err := handler.FreezeTokens(ctx, req2)

	require.NoError(t, err)
	require.NotNil(t, resp2)
}

func TestUnfreezeTokens_SuccessWhenFrozen(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)

	freezeReq := createFreezeTestRequest(t, cfg, tokenCreate, false)
	freezeResp, err := handler.FreezeTokens(ctx, freezeReq)
	require.NoError(t, err)
	require.NotNil(t, freezeResp)

	unfreezeReq := createFreezeTestRequest(t, cfg, tokenCreate, true)
	unfreezeResp, err := handler.FreezeTokens(ctx, unfreezeReq)

	require.NoError(t, err)
	require.NotNil(t, unfreezeResp)
}

func TestUnfreezeTokens_IdempotentWhenNotFrozen(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)
	req := createFreezeTestRequest(t, cfg, tokenCreate, true)

	// Unfreezing when not frozen should succeed (idempotent)
	resp, err := handler.FreezeTokens(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestUnfreezeTokens_FailsWhenNotFreezable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, false)
	req := createFreezeTestRequest(t, cfg, tokenCreate, true)

	resp, err := handler.FreezeTokens(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), tokens.ErrTokenNotFreezable)
}

func TestFreezeTokens_FailsWithInvalidSignature(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)
	req := createFreezeTestRequestWithKey(t, cfg, tokenCreate, false, freezeTestWrongKey)

	resp, err := handler.FreezeTokens(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "invalid issuer signature")
}

// Timestamp-based replay protection tests

func TestFreezeTokens_RejectsStaleFreeze(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)

	// First freeze with timestamp 1000
	freezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, 1000)
	_, err := handler.FreezeTokens(ctx, freezeReq)
	require.NoError(t, err)

	// Unfreeze with timestamp 2000
	unfreezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, 2000)
	_, err = handler.FreezeTokens(ctx, unfreezeReq)
	require.NoError(t, err)

	// Try to freeze again with timestamp 1500 (older than thaw) - should be rejected
	staleReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, 1500)
	resp, err := handler.FreezeTokens(ctx, staleReq)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "stale freeze request")
}

func TestFreezeTokens_RejectsStaleUnfreeze(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)

	// Freeze with timestamp 2000
	freezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, 2000)
	_, err := handler.FreezeTokens(ctx, freezeReq)
	require.NoError(t, err)

	// Try to unfreeze with timestamp 1000 (older than freeze) - should be rejected
	staleReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, 1000)
	resp, err := handler.FreezeTokens(ctx, staleReq)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "stale unfreeze request")
}

func TestFreezeTokens_AcceptsNewerTimestamp(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, cfg, true)

	// Freeze with timestamp 1000
	freezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, 1000)
	_, err := handler.FreezeTokens(ctx, freezeReq)
	require.NoError(t, err)

	// Unfreeze with timestamp 2000 - should succeed
	unfreezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, 2000)
	_, err = handler.FreezeTokens(ctx, unfreezeReq)
	require.NoError(t, err)

	// Freeze again with timestamp 3000 - should succeed
	freezeReq2 := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, 3000)
	resp, err := handler.FreezeTokens(ctx, freezeReq2)

	require.NoError(t, err)
	require.NotNil(t, resp)
}
