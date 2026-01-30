package tokens

import (
	"context"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	mathrand "math/rand/v2"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

var (
	freezeTestRng       = mathrand.NewChaCha8([32]byte{0xFE, 0xED})
	freezeTestIssuerKey = keys.MustGeneratePrivateKeyFromRand(freezeTestRng)
	freezeTestOwnerKey  = keys.MustGeneratePrivateKeyFromRand(freezeTestRng)
	freezeTestWrongKey  = keys.MustGeneratePrivateKeyFromRand(freezeTestRng)
)

// recentTimestamp returns a timestamp (in millis) that is the given duration before now.
// Use this instead of hardcoded timestamps to ensure timestamps pass validation.
func recentTimestamp(ago time.Duration) uint64 {
	return uint64(time.Now().Add(-ago).UnixMilli())
}

type mockFreezeInternalServer struct {
	tokeninternalpb.UnimplementedSparkTokenInternalServiceServer
	errToReturn error
}

func (s *mockFreezeInternalServer) InternalFreezeTokens(
	_ context.Context,
	_ *tokeninternalpb.InternalFreezeTokensRequest,
) (*tokeninternalpb.InternalFreezeTokensResponse, error) {
	if s.errToReturn != nil {
		return nil, s.errToReturn
	}
	return &tokeninternalpb.InternalFreezeTokensResponse{
		ImpactedTokenOutputs: []*tokenpb.TokenOutputRef{},
		ImpactedTokenAmount:  big.NewInt(0).Bytes(),
	}, nil
}

func startMockFreezeGRPCServer(t *testing.T, mockServer *mockFreezeInternalServer) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	t.Cleanup(func() { _ = l.Close() })

	server := grpc.NewServer()
	tokeninternalpb.RegisterSparkTokenInternalServiceServer(server, mockServer)
	go func() {
		if err := server.Serve(l); err != nil {
			t.Logf("Mock freeze gRPC server error: %v", err)
		}
	}()
	t.Cleanup(server.Stop)
	return addr
}

type coordinatedFreezeTestSetup struct {
	ctx         context.Context
	tc          *db.TestContext
	cfg         *so.Config
	handler     *FreezeTokenHandler
	tokenCreate *ent.TokenCreate
	mockServers []*mockFreezeInternalServer
}

func setupCoordinatedFreezeTest(t *testing.T, mockServerCount int, mockErrors []error) *coordinatedFreezeTestSetup {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)

	knobService := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobCoordinatedFreezeEnabled: 1.0,
	})
	ctx = knobs.InjectKnobsService(ctx, knobService)

	selfIdentifier := cfg.Identifier
	selfIdentityPubKey := cfg.IdentityPrivateKey.Public()

	cfg.SigningOperatorMap = make(map[string]*so.SigningOperator)
	cfg.SigningOperatorMap[selfIdentifier] = &so.SigningOperator{
		Identifier:        selfIdentifier,
		IdentityPublicKey: selfIdentityPubKey,
	}

	var mockServers []*mockFreezeInternalServer
	for i := range mockServerCount {
		var errToReturn error
		if i < len(mockErrors) {
			errToReturn = mockErrors[i]
		}

		mockServer := &mockFreezeInternalServer{
			errToReturn: errToReturn,
		}
		mockServers = append(mockServers, mockServer)

		mockPrivKey := keys.GeneratePrivateKey()
		mockAddr := startMockFreezeGRPCServer(t, mockServer)
		mockIdentifier := so.IndexToIdentifier(uint32(i + 1))
		cfg.SigningOperatorMap[mockIdentifier] = &so.SigningOperator{
			Identifier:                mockIdentifier,
			IdentityPublicKey:         mockPrivKey.Public(),
			AddressRpc:                mockAddr,
			OperatorConnectionFactory: &sparktesting.DangerousTestOperatorConnectionFactoryNoTLS{},
		}
	}

	handler := NewFreezeTokenHandler(cfg)
	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	return &coordinatedFreezeTestSetup{
		ctx:         ctx,
		tc:          tc,
		cfg:         cfg,
		handler:     handler,
		tokenCreate: tokenCreate,
		mockServers: mockServers,
	}
}

func createFreezeTestTokenCreate(t *testing.T, ctx context.Context, client *ent.Client, isFreezable bool) *ent.TokenCreate {
	t.Helper()
	fixtures := entfixtures.New(t, ctx, client)
	_, tokenCreate := fixtures.CreateTokenCreateWithOpts(btcnetwork.Regtest, entfixtures.TokenCreateOpts{
		IssuerKey:   freezeTestIssuerKey,
		IsFreezable: isFreezable,
	})
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
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)
	req := createFreezeTestRequest(t, cfg, tokenCreate, false)

	resp, err := handler.FreezeTokens(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.ImpactedTokenOutputs)
	assert.Equal(t, big.NewInt(0).Bytes(), resp.ImpactedTokenAmount)
}

func TestFreezeTokens_FailsWhenNotFreezable(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, false)
	req := createFreezeTestRequest(t, cfg, tokenCreate, false)

	resp, err := handler.FreezeTokens(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), tokens.ErrTokenNotFreezable)
}

func TestFreezeTokens_IdempotentWhenAlreadyFrozen(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	// Use same timestamp for both requests to test idempotency
	timestamp := recentTimestamp(10 * time.Second)
	req1 := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, timestamp)
	resp1, err := handler.FreezeTokens(ctx, req1)
	require.NoError(t, err)
	require.NotNil(t, resp1)

	req2 := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, timestamp)
	resp2, err := handler.FreezeTokens(ctx, req2)

	require.NoError(t, err)
	require.NotNil(t, resp2)
}

func TestFreezeTokens_RejectsDifferentTimestampWhenAlreadyFrozen(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	req1 := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, recentTimestamp(20*time.Second))
	_, err := handler.FreezeTokens(ctx, req1)
	require.NoError(t, err)

	// Freezing with a different timestamp should fail
	req2 := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, recentTimestamp(10*time.Second))
	resp, err := handler.FreezeTokens(ctx, req2)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "already frozen")
}

func TestUnfreezeTokens_SuccessWhenFrozen(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

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
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)
	req := createFreezeTestRequest(t, cfg, tokenCreate, true)

	resp, err := handler.FreezeTokens(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestUnfreezeTokens_IdempotentWhenAlreadyUnfrozen(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	// Freeze then unfreeze (older timestamp first, then newer)
	freezeTs := recentTimestamp(30 * time.Second)
	unfreezeTs := recentTimestamp(20 * time.Second)
	freezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, freezeTs)
	_, err := handler.FreezeTokens(ctx, freezeReq)
	require.NoError(t, err)

	unfreezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, unfreezeTs)
	_, err = handler.FreezeTokens(ctx, unfreezeReq)
	require.NoError(t, err)

	// Unfreezing again with same timestamp should succeed (idempotent)
	unfreezeReq2 := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, unfreezeTs)
	resp, err := handler.FreezeTokens(ctx, unfreezeReq2)

	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestUnfreezeTokens_RejectsDifferentTimestampWhenAlreadyUnfrozen(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	// Freeze then unfreeze (older timestamp first, then newer)
	freezeTs := recentTimestamp(30 * time.Second)
	unfreezeTs := recentTimestamp(20 * time.Second)
	freezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, freezeTs)
	_, err := handler.FreezeTokens(ctx, freezeReq)
	require.NoError(t, err)

	unfreezeReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, unfreezeTs)
	_, err = handler.FreezeTokens(ctx, unfreezeReq)
	require.NoError(t, err)

	// Unfreezing with a different timestamp should fail
	unfreezeReq2 := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, recentTimestamp(10*time.Second))
	resp, err := handler.FreezeTokens(ctx, unfreezeReq2)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "already unfrozen")
}

func TestUnfreezeTokens_FailsWhenNotFreezable(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, false)
	req := createFreezeTestRequest(t, cfg, tokenCreate, true)

	resp, err := handler.FreezeTokens(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), tokens.ErrTokenNotFreezable)
}

func TestFreezeTokens_FailsWithInvalidSignature(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)
	req := createFreezeTestRequestWithKey(t, cfg, tokenCreate, false, freezeTestWrongKey)

	resp, err := handler.FreezeTokens(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "invalid issuer signature")
}

// Timestamp validation tests

func TestFreezeTokens_TimestampValidation(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	// Future timestamp (2 minutes from now) should be rejected
	futureReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, uint64(time.Now().Add(2*time.Minute).UnixMilli()))
	_, err := handler.FreezeTokens(ctx, futureReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too far in the future")

	// Old timestamp (2 minutes ago) should be rejected
	oldReq := createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, uint64(time.Now().Add(-2*time.Minute).UnixMilli()))
	_, err = handler.FreezeTokens(ctx, oldReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too old")
}

func TestFreezeTokens_RejectsStaleUnfreeze(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	// Freeze -> unfreeze -> freeze with increasing timestamps
	freezeTs := recentTimestamp(30 * time.Second)
	unfreezeTs := recentTimestamp(20 * time.Second)
	refreezeTs := recentTimestamp(10 * time.Second)

	_, err := handler.FreezeTokens(ctx, createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, freezeTs))
	require.NoError(t, err)

	_, err = handler.FreezeTokens(ctx, createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, unfreezeTs))
	require.NoError(t, err)

	_, err = handler.FreezeTokens(ctx, createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, refreezeTs))
	require.NoError(t, err)

	// Stale unfreeze (older than current freeze) should be rejected
	_, err = handler.FreezeTokens(ctx, createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, unfreezeTs))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale unfreeze request")
}

func TestFreezeTokens_RejectsStaleFreeze(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewFreezeTokenHandler(cfg)

	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)

	// Freeze -> unfreeze with increasing timestamps
	freezeTs := recentTimestamp(30 * time.Second)
	unfreezeTs := recentTimestamp(20 * time.Second)

	_, err := handler.FreezeTokens(ctx, createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, freezeTs))
	require.NoError(t, err)

	_, err = handler.FreezeTokens(ctx, createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, true, freezeTestIssuerKey, unfreezeTs))
	require.NoError(t, err)

	// Stale freeze (older than most recent thaw) should be rejected
	staleFreezeTs := recentTimestamp(25 * time.Second) // Between freezeTs and unfreezeTs, but older than unfreezeTs
	_, err = handler.FreezeTokens(ctx, createFreezeTestRequestWithTimestamp(t, cfg, tokenCreate, false, freezeTestIssuerKey, staleFreezeTs))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale freeze request")
}

func TestFreezeTokens_CoordinatedFreezeAllSuccess(t *testing.T) {
	setup := setupCoordinatedFreezeTest(t, 2, nil)
	req := createFreezeTestRequest(t, setup.cfg, setup.tokenCreate, false)

	resp, err := setup.handler.FreezeTokens(setup.ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.FreezeProgress)
	// Self + 2 mock operators = 3 total frozen
	assert.Len(t, resp.FreezeProgress.FrozenOperatorPublicKeys, 3)
	assert.Empty(t, resp.FreezeProgress.UnfrozenOperatorPublicKeys)
}

func TestFreezeTokens_CoordinatedFreezeAllOthersFailed(t *testing.T) {
	mockErrors := []error{
		errors.New("mock operator 1 failed"),
		errors.New("mock operator 2 failed"),
	}
	setup := setupCoordinatedFreezeTest(t, 2, mockErrors)
	req := createFreezeTestRequest(t, setup.cfg, setup.tokenCreate, false)

	resp, err := setup.handler.FreezeTokens(setup.ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.FreezeProgress)
	// Only self succeeded
	assert.Len(t, resp.FreezeProgress.FrozenOperatorPublicKeys, 1)
	// 2 mock operators failed
	assert.Len(t, resp.FreezeProgress.UnfrozenOperatorPublicKeys, 2)
}

func TestFreezeTokens_CoordinatedFreezePartialSuccess(t *testing.T) {
	mockErrors := []error{
		nil, // First mock operator succeeds
		errors.New("mock operator 2 failed"),
	}
	setup := setupCoordinatedFreezeTest(t, 2, mockErrors)
	req := createFreezeTestRequest(t, setup.cfg, setup.tokenCreate, false)

	resp, err := setup.handler.FreezeTokens(setup.ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.FreezeProgress)
	// Self + 1 mock operator succeeded, 1 failed
	// Total frozen should be 2, unfrozen should be 1
	totalOperators := len(resp.FreezeProgress.FrozenOperatorPublicKeys) + len(resp.FreezeProgress.UnfrozenOperatorPublicKeys)
	assert.Equal(t, 3, totalOperators)
	assert.GreaterOrEqual(t, len(resp.FreezeProgress.FrozenOperatorPublicKeys), 1) // At least self succeeded
}

func TestFreezeTokens_CoordinatedFreezeDisabled(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)

	// Coordinated freeze is disabled by default (no knob service set)
	handler := NewFreezeTokenHandler(cfg)
	tokenCreate := createFreezeTestTokenCreate(t, ctx, tc.Client, true)
	req := createFreezeTestRequest(t, cfg, tokenCreate, false)

	resp, err := handler.FreezeTokens(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	// No freeze progress when coordinated freeze is disabled
	assert.Nil(t, resp.FreezeProgress)
}
