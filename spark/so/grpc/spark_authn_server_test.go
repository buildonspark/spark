package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/lightsparkdev/spark-go/proto/spark_authn"
	pb_authn_internal "github.com/lightsparkdev/spark-go/proto/spark_authn_internal"
	"github.com/lightsparkdev/spark-go/so/authn"
	"github.com/lightsparkdev/spark-go/so/authninternal"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

var (
	testIdentityKey, _   = secp256k1.GeneratePrivateKey()
	testIdentityKeyBytes = testIdentityKey.Serialize()
)

const (
	testChallengeTimeout = time.Minute
	testSessionDuration  = 24 * time.Hour
)

type testServerConfig struct {
	clock authninternal.Clock
}

// newTestServerAndTokenVerifier creates an AuthenticationServer and SessionTokenCreatorVerifier with default test configuration
func newTestServerAndTokenVerifier(
	t *testing.T,
	opts ...func(*testServerConfig),
) (*AuthnServer, *authninternal.SessionTokenCreatorVerifier) {
	cfg := &testServerConfig{
		clock: authninternal.RealClock{},
	}

	// Apply options
	for _, opt := range opts {
		opt(cfg)
	}

	tokenVerifier, err := authninternal.NewSessionTokenCreatorVerifier(testIdentityKeyBytes, cfg.clock)
	require.NoError(t, err)

	config := AuthnServerConfig{
		IdentityPrivateKey: testIdentityKeyBytes,
		ChallengeTimeout:   testChallengeTimeout,
		SessionDuration:    testSessionDuration,
		Clock:              cfg.clock,
	}

	server, err := NewAuthnServer(config, tokenVerifier)
	require.NoError(t, err)

	return server, tokenVerifier
}

func withClock(clock authninternal.Clock) func(*testServerConfig) {
	return func(cfg *testServerConfig) {
		cfg.clock = clock
	}
}

func TestSparkAuthnServer_GetChallenge_InvalidPublicKey(t *testing.T) {
	tests := []struct {
		name   string
		pubkey []byte
	}{
		{
			name:   "empty pubkey",
			pubkey: []byte{},
		},
		{
			name:   "malformed pubkey",
			pubkey: []byte{0x02, 0x03},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newTestServerAndTokenVerifier(t)

			_, err := server.GetChallenge(context.Background(), &pb.GetChallengeRequest{
				PublicKey: tt.pubkey,
			})

			assert.ErrorIs(t, err, ErrInvalidPublicKeyFormat)
		})
	}
}

func TestSparkAuthnServer_VerifyChallenge_ValidToken(t *testing.T) {
	clock := authninternal.NewTestClock(time.Now())
	server, tokenVerifier := newTestServerAndTokenVerifier(t, withClock(clock))
	privKey, pubKey := createTestKeyPair()

	challengeResp, signature := createSignedChallenge(t, server, privKey)
	verifyResp := verifyChallenge(t, server, challengeResp, pubKey, signature)

	assert.NotNil(t, verifyResp)
	assert.NotEmpty(t, verifyResp.SessionToken)

	authnInterceptor := authn.NewAuthnInterceptor(tokenVerifier)

	// Make a request with the expired token
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+verifyResp.SessionToken,
	))
	var capturedCtx context.Context
	authnInterceptor.AuthnInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ interface{}) (interface{}, error) { //nolint:errcheck
		capturedCtx = ctx
		return nil, nil
	})

	session, err := authn.GetSessionFromContext(capturedCtx)
	require.NoError(t, err)
	assert.Equal(t, session.IdentityPublicKey(), pubKey)
	assert.Equal(t, session.IdentityPublicKeyBytes(), pubKey.SerializeCompressed())
	assert.Equal(t, session.ExpirationTimestamp(), clock.Now().Add(testSessionDuration).Unix())
}

func TestSparkAuthnServer_VerifyChallenge_InvalidSignature(t *testing.T) {
	server, _ := newTestServerAndTokenVerifier(t)
	privKey, pubKey := createTestKeyPair()

	challengeResp, _ := createSignedChallenge(t, server, privKey)

	wrongPrivKey, _ := createTestKeyPair()
	challengeBytes, _ := proto.Marshal(challengeResp.ProtectedChallenge.Challenge)
	hash := sha256.Sum256(challengeBytes)
	wrongSignature := ecdsa.Sign(wrongPrivKey, hash[:])

	resp, err := server.VerifyChallenge(
		context.Background(),
		&pb.VerifyChallengeRequest{
			ProtectedChallenge: challengeResp.ProtectedChallenge,
			Signature:          wrongSignature.Serialize(),
			PublicKey:          pubKey.SerializeCompressed(),
		},
	)

	assert.ErrorIs(t, err, ErrInvalidSignature)
	assert.Nil(t, resp)
}

func TestSparkAuthnServer_VerifyChallenge_ExpiredSessionToken(t *testing.T) {
	clock := authninternal.NewTestClock(time.Now())
	server, tokenVerifier := newTestServerAndTokenVerifier(t, withClock(clock))
	privKey, pubKey := createTestKeyPair()

	challengeResp, signature := createSignedChallenge(t, server, privKey)
	resp := verifyChallenge(t, server, challengeResp, pubKey, signature)

	clock.Advance(testSessionDuration + time.Second)

	authnInterceptor := authn.NewAuthnInterceptor(tokenVerifier)

	// Make a request with the expired token
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+resp.SessionToken,
	))
	authnInterceptor.AuthnInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(_ context.Context, _ interface{}) (interface{}, error) { //nolint:errcheck
		return nil, nil
	})

	noSession, err := authn.GetSessionFromContext(ctx)
	assert.Error(t, err)
	assert.Nil(t, noSession)
}

func TestSparkAuthnServer_VerifyChallenge_ExpiredChallenge(t *testing.T) {
	clock := authninternal.NewTestClock(time.Now())
	server, _ := newTestServerAndTokenVerifier(t, withClock(clock))
	privKey, pubKey := createTestKeyPair()

	challengeResp, signature := createSignedChallenge(t, server, privKey)

	clock.Advance(testChallengeTimeout + time.Second)

	resp, err := server.VerifyChallenge(
		context.Background(),
		&pb.VerifyChallengeRequest{
			ProtectedChallenge: challengeResp.ProtectedChallenge,
			Signature:          signature,
			PublicKey:          pubKey.SerializeCompressed(),
		},
	)

	assert.ErrorIs(t, err, ErrChallengeExpired)
	assert.Nil(t, resp)
}

func TestSparkAuthnServer_VerifyChallenge_TamperedToken(t *testing.T) {
	server, tokenVerifier := newTestServerAndTokenVerifier(t)
	privKey, pubKey := createTestKeyPair()

	challengeResp, signature := createSignedChallenge(t, server, privKey)
	verifyResp := verifyChallenge(t, server, challengeResp, pubKey, signature)

	sessionToken := verifyResp.SessionToken
	protectedBytes, _ := base64.URLEncoding.DecodeString(sessionToken)

	protected := &pb_authn_internal.ProtectedSession{}
	proto.Unmarshal(protectedBytes, protected) //nolint:errcheck

	tests := []struct {
		name        string
		tamper      func(protected *pb_authn_internal.ProtectedSession)
		wantErrType error
	}{
		{
			name: "tampered nonce",
			tamper: func(protected *pb_authn_internal.ProtectedSession) {
				protected.Session.Nonce = []byte("tampered nonce")
			},
			wantErrType: authninternal.ErrInvalidTokenHmac,
		},
		{
			name: "change key",
			tamper: func(protected *pb_authn_internal.ProtectedSession) {
				protected.Session.PublicKey = []byte("tampered key")
			},
			wantErrType: authninternal.ErrInvalidTokenHmac,
		},
		{
			name: "tampered session protection version",
			tamper: func(protected *pb_authn_internal.ProtectedSession) {
				protected.Version = 999
			},
			wantErrType: authninternal.ErrUnsupportedProtectionVersion,
		},
		{
			name: "tampered session version",
			tamper: func(protected *pb_authn_internal.ProtectedSession) {
				protected.Session.Version = 999
			},
			wantErrType: authninternal.ErrUnsupportedSessionVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protectedSubject := proto.Clone(protected).(*pb_authn_internal.ProtectedSession)
			tt.tamper(protectedSubject)
			tamperedBytes, err := proto.Marshal(protectedSubject)
			require.NoError(t, err)
			tamperedToken := base64.URLEncoding.EncodeToString(tamperedBytes)

			_, err = tokenVerifier.VerifyToken(tamperedToken)

			assert.ErrorIs(t, err, tt.wantErrType)
		})
	}
}

// Test helpers
func createTestKeyPair() (*secp256k1.PrivateKey, *secp256k1.PublicKey) {
	privKey, _ := secp256k1.GeneratePrivateKey()
	return privKey, privKey.PubKey()
}

func createSignedChallenge(t *testing.T, server *AuthnServer, privKey *secp256k1.PrivateKey) (*pb.GetChallengeResponse, []byte) {
	pubKey := privKey.PubKey()

	challengeResp, err := server.GetChallenge(context.Background(), &pb.GetChallengeRequest{
		PublicKey: pubKey.SerializeCompressed(),
	})
	require.NoError(t, err)

	challengeBytes, err := proto.Marshal(challengeResp.ProtectedChallenge.Challenge)
	require.NoError(t, err)

	hash := sha256.Sum256(challengeBytes)
	signature := ecdsa.Sign(privKey, hash[:])

	return challengeResp, signature.Serialize()
}

func verifyChallenge(t *testing.T, server *AuthnServer, challengeResp *pb.GetChallengeResponse, pubKey *secp256k1.PublicKey, signature []byte) *pb.VerifyChallengeResponse {
	resp, err := server.VerifyChallenge(
		context.Background(),
		&pb.VerifyChallengeRequest{
			ProtectedChallenge: challengeResp.ProtectedChallenge,
			Signature:          signature,
			PublicKey:          pubKey.SerializeCompressed(),
		},
	)
	require.NoError(t, err)
	return resp
}

func assertNoSessionInContext(ctx context.Context, t *testing.T) {
	t.Helper()
	var capturedCtx context.Context
	authnInterceptor := authn.NewAuthnInterceptor(newTestTokenVerifier(t))

	_, err := authnInterceptor.AuthnInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ interface{}) (interface{}, error) {
		capturedCtx = ctx
		return nil, nil
	})

	require.NoError(t, err)
	noSession, err := authn.GetSessionFromContext(capturedCtx)
	assert.Nil(t, noSession)
	assert.Error(t, err)
}

// Helper to create a test token verifier
func newTestTokenVerifier(t *testing.T) *authninternal.SessionTokenCreatorVerifier {
	tokenVerifier, err := authninternal.NewSessionTokenCreatorVerifier(testIdentityKeyBytes, authninternal.RealClock{})
	require.NoError(t, err)
	return tokenVerifier
}

func TestSparkAuthnServer_VerifyChallenge_InvalidAuth(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{
			name: "no metadata",
			ctx:  context.Background(),
		},
		{
			name: "empty metadata",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.MD{}),
		},
		{
			name: "empty auth header",
			ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", "",
			)),
		},
		{
			name: "missing bearer prefix",
			ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", "INVALID_SESSION_TOKEN",
			)),
		},
		{
			name: "invalid token format",
			ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", "Bearer INVALID_SESSION_TOKEN",
			)),
		},
		{
			name: "malformed base64",
			ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", "Bearer not-base64!@#$",
			)),
		},
		{
			name: "empty bearer token",
			ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", "Bearer ",
			)),
		},
		{
			name: "valid base64 but invalid proto",
			ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", "Bearer "+base64.URLEncoding.EncodeToString([]byte("not-a-proto")),
			)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNoSessionInContext(tt.ctx, t)
		})
	}
}
