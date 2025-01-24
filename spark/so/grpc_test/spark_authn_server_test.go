package grpctest

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
	"github.com/lightsparkdev/spark-go/so/grpc"
	"google.golang.org/protobuf/proto"
)

var testIdentityKey, _ = secp256k1.GeneratePrivateKey()
var testIdentityKeyBytes = testIdentityKey.Serialize()

const (
	testChallengeTimeout = time.Minute
	testSessionDuration  = 24 * time.Hour
)

type testServerConfig struct {
	clock authn.Clock
}

// newTestServerAndTokenVerifier creates an AuthenticationServer and SessionTokenCreatorVerifier with default test configuration
func newTestServerAndTokenVerifier(
	t *testing.T,
	opts ...func(*testServerConfig),
) (*grpc.AuthnServer, *authn.SessionTokenCreatorVerifier) {
	cfg := &testServerConfig{
		clock: authn.RealClock{},
	}

	// Apply options
	for _, opt := range opts {
		opt(cfg)
	}

	tokenVerifier, err := authn.NewSessionTokenCreatorVerifier(testIdentityKeyBytes, cfg.clock)
	require.NoError(t, err)

	config := grpc.AuthnServerConfig{
		IdentityPrivateKey: testIdentityKeyBytes,
		ChallengeTimeout:   testChallengeTimeout,
		SessionDuration:    testSessionDuration,
		Clock:              cfg.clock,
	}

	server, err := grpc.NewAuthnServer(config, tokenVerifier)
	require.NoError(t, err)

	return server, tokenVerifier
}

func withClock(clock authn.Clock) func(*testServerConfig) {
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

			assert.ErrorIs(t, err, grpc.ErrInvalidPublicKeyFormat)
		})
	}
}

func TestSparkAuthnServer_VerifyChallenge_ValidToken(t *testing.T) {
	server, tokenVerifier := newTestServerAndTokenVerifier(t)
	privKey, pubKey := createTestKeyPair()

	challengeResp, signature := createSignedChallenge(t, server, privKey)
	verifyResp := verifyChallenge(t, server, challengeResp, pubKey, signature)

	assert.NotNil(t, verifyResp)
	assert.NotEmpty(t, verifyResp.SessionToken)

	session, err := tokenVerifier.VerifyToken(verifyResp.SessionToken)
	require.NoError(t, err)
	assert.Equal(t, session.PublicKey, pubKey.SerializeCompressed())
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

	assert.ErrorIs(t, err, grpc.ErrInvalidSignature)
	assert.Nil(t, resp)
}

func TestSparkAuthnServer_VerifyChallenge_ExpiredSessionToken(t *testing.T) {
	clock := authn.NewTestClock(time.Now())
	server, tokenVerifier := newTestServerAndTokenVerifier(t, withClock(clock))
	privKey, pubKey := createTestKeyPair()

	challengeResp, signature := createSignedChallenge(t, server, privKey)
	resp := verifyChallenge(t, server, challengeResp, pubKey, signature)

	clock.Advance(testSessionDuration + time.Second)

	session, err := tokenVerifier.VerifyToken(resp.SessionToken)

	assert.ErrorIs(t, err, authn.ErrTokenExpired)
	assert.Nil(t, session)
}

func TestSparkAuthnServer_VerifyChallenge_ExpiredChallenge(t *testing.T) {
	clock := authn.NewTestClock(time.Now())
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

	assert.ErrorIs(t, err, grpc.ErrChallengeExpired)
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
	proto.Unmarshal(protectedBytes, protected)

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
			wantErrType: authn.ErrInvalidTokenHmac,
		},
		{
			name: "change key",
			tamper: func(protected *pb_authn_internal.ProtectedSession) {
				protected.Session.PublicKey = []byte("tampered key")
			},
			wantErrType: authn.ErrInvalidTokenHmac,
		},
		{
			name: "tampered session protection version",
			tamper: func(protected *pb_authn_internal.ProtectedSession) {
				protected.Version = 999
			},
			wantErrType: authn.ErrUnsupportedProtectionVersion,
		},
		{
			name: "tampered session version",
			tamper: func(protected *pb_authn_internal.ProtectedSession) {
				protected.Session.Version = 999
			},
			wantErrType: authn.ErrUnsupportedSessionVersion,
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

func createSignedChallenge(t *testing.T, server *grpc.AuthnServer, privKey *secp256k1.PrivateKey) (*pb.GetChallengeResponse, []byte) {
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

func verifyChallenge(t *testing.T, server *grpc.AuthnServer, challengeResp *pb.GetChallengeResponse, pubKey *secp256k1.PublicKey, signature []byte) *pb.VerifyChallengeResponse {
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
