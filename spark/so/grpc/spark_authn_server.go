package grpc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark_authn"
	"github.com/lightsparkdev/spark/so/authninternal"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/patrickmn/go-cache"
	"google.golang.org/protobuf/proto"
)

const (
	currentChallengeVersion  = 1
	currentProtectionVersion = 1
	challengeSecretConstant  = "AUTH_CHALLENGE_SECRET_v1"
	clockSkewSeconds         = 1
)

// challengeNonceCache tracks used nonces to prevent reuse
type challengeNonceCache struct {
	cache *cache.Cache
}

func newChallengeNonceCache(challengeTimeout time.Duration) *challengeNonceCache {
	cleanupInterval := challengeTimeout / 10
	if cleanupInterval < 10*time.Second {
		cleanupInterval = 10 * time.Second
	}
	return &challengeNonceCache{
		cache: cache.New(challengeTimeout, cleanupInterval),
	}
}

// markNonceUsed marks a nonce as used. Returns ErrChallengeReused if the nonce was already used.
func (cnc *challengeNonceCache) markNonceUsed(ctx context.Context, nonce []byte) error {
	logger := logging.GetLoggerFromContext(ctx)

	// Add is atomic - returns nil only if the key didn't exist and was added
	err := cnc.cache.Add(string(nonce), true, cache.DefaultExpiration)
	if err != nil {
		logger.Warn("nonce reuse detected",
			"nonce", fmt.Sprintf("%x", nonce),
			"error", err)
		return ErrChallengeReused
	}
	return nil
}

// generateNonce generates a new nonce for use in a challenge
func (cnc *challengeNonceCache) generateNonce() ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate secure nonce: %w", err)
	}
	return nonce, nil
}

// AuthnServerConfig contains the configuration for the AuthenticationServer
type AuthnServerConfig struct {
	// Server's secp256k1 private key for identity
	IdentityPrivateKey keys.Private
	// Challenge validity duration
	ChallengeTimeout time.Duration
	// Session duration
	SessionDuration time.Duration
	// Clock to use for time-based operations
	Clock authninternal.Clock
}

// AuthnServer implements the SparkAuthnServiceServer interface
type AuthnServer struct {
	pb.UnimplementedSparkAuthnServiceServer
	config                      AuthnServerConfig
	challengeHmacKey            []byte
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier
	clock                       authninternal.Clock
	nonceCache                  *challengeNonceCache
}

var (
	// ErrUnsupportedChallengeVersion is returned when the challenge version is unsupported.
	ErrUnsupportedChallengeVersion = errors.New("unsupported challenge version")
	// ErrUnsupportedChallengeProtectionVersion is returned when the challenge protection version is unsupported.
	ErrUnsupportedChallengeProtectionVersion = errors.New("unsupported challenge protection version")
	// ErrChallengeExpired is returned when the challenge has expired.
	ErrChallengeExpired = errors.New("challenge expired")
	// ErrChallengeReused is returned when the challenge has been reused.
	ErrChallengeReused = errors.New("challenge has already been used")
	// ErrPublicKeyMismatch is returned when the public key does not match the challenge.
	ErrPublicKeyMismatch = errors.New("public key does not match challenge")
	// ErrInvalidChallengeHmac is returned when the challenge hmac is invalid.
	ErrInvalidChallengeHmac = errors.New("invalid challenge hmac")
	// ErrInvalidPublicKeyFormat is returned when the public key format is invalid.
	ErrInvalidPublicKeyFormat = errors.New("invalid public key format")
	// ErrInvalidSignature is returned when the client signature is invalid.
	ErrInvalidSignature = errors.New("invalid client signature")
	// ErrInternalError is returned when an unexpected internal error occurs.
	ErrInternalError = errors.New("internal error")
)

// NewAuthnServer creates a new AuthnServer.
// If the clock is nil, it will use the real clock.
func NewAuthnServer(
	config AuthnServerConfig,
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier,
) (*AuthnServer, error) {
	if config.Clock == nil {
		config.Clock = authninternal.RealClock{}
	}

	if config.IdentityPrivateKey.IsZero() {
		return nil, fmt.Errorf("%w: identity private key is required", ErrInternalError)
	}

	if config.ChallengeTimeout < time.Second {
		return nil, fmt.Errorf("%w: challenge timeout must be at least one second", ErrInternalError)
	}

	// Derive challenge HMAC key from identity key and constant
	h := sha256.New()
	h.Write(config.IdentityPrivateKey.Serialize())
	h.Write([]byte(challengeSecretConstant))
	challengeHmacKey := h.Sum(nil)

	return &AuthnServer{
		config:                      config,
		challengeHmacKey:            challengeHmacKey,
		sessionTokenCreatorVerifier: sessionTokenCreatorVerifier,
		clock:                       config.Clock,
		nonceCache:                  newChallengeNonceCache(config.ChallengeTimeout),
	}, nil
}

// GetChallenge generates a new challenge for the given public key.
// This is the first step of the authentication process.
func (s *AuthnServer) GetChallenge(_ context.Context, req *pb.GetChallengeRequest) (*pb.GetChallengeResponse, error) {
	if req == nil {
		return nil, sparkerrors.InvalidUserInputErrorf("invalid request: request cannot be nil")
	}

	_, err := keys.ParsePublicKey(req.PublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidUserInputErrorf("invalid public key format: %w", err)
	}
	nonce, err := s.nonceCache.generateNonce()
	if err != nil {
		return nil, fmt.Errorf("internal error: failed to generate nonce: %w", err)
	}

	challenge := &pb.Challenge{
		Version:   currentChallengeVersion,
		Timestamp: s.clock.Now().Unix(),
		Nonce:     nonce,
		PublicKey: req.PublicKey,
	}

	challengeBytes, err := proto.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("internal error: failed to serialize challenge: %w", err)
	}

	mac := s.computeChallengeHmac(challengeBytes)

	protectedChallenge := &pb.ProtectedChallenge{
		Version:    currentProtectionVersion,
		Challenge:  challenge,
		ServerHmac: mac,
	}

	response := &pb.GetChallengeResponse{
		ProtectedChallenge: protectedChallenge,
	}

	return response, nil
}

// VerifyChallenge verifies the client's signature on the challenge and returns a session token.
// This is the second step of the authentication process.
func (s *AuthnServer) VerifyChallenge(ctx context.Context, req *pb.VerifyChallengeRequest) (*pb.VerifyChallengeResponse, error) {
	if req == nil {
		return nil, sparkerrors.InvalidUserInputErrorf("invalid request: request cannot be nil")
	}

	if req.ProtectedChallenge == nil {
		return nil, sparkerrors.InvalidUserInputErrorf("invalid request: protected challenge cannot be nil")
	}

	if req.ProtectedChallenge.Challenge == nil {
		return nil, sparkerrors.InvalidUserInputErrorf("invalid request: challenge cannot be nil")
	}

	if len(req.Signature) == 0 {
		return nil, sparkerrors.InvalidUserInputErrorf("invalid request: signature cannot be empty")
	}

	pubKey, err := keys.ParsePublicKey(req.PublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidUserInputErrorf("invalid public key format: %w", err)
	}

	challenge := req.ProtectedChallenge.Challenge

	if err := s.validateChallenge(ctx, challenge, req); err != nil {
		return nil, fmt.Errorf("challenge validation failed: %w", err)
	}

	challengeBytes, err := proto.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("internal error: failed to serialize challenge: %w", err)
	}

	if err := s.verifyChallengeHmac(challengeBytes, req.ProtectedChallenge.ServerHmac); err != nil {
		return nil, fmt.Errorf("challenge verification failed: %w", err)
	}

	if err := s.verifyClientSignature(challengeBytes, pubKey, req.Signature); err != nil {
		return nil, fmt.Errorf("signature verification failed: %w", err)
	}

	result, err := s.sessionTokenCreatorVerifier.CreateToken(pubKey, s.config.SessionDuration)
	if err != nil {
		return nil, fmt.Errorf("internal error: failed to create session token: %w", err)
	}

	return &pb.VerifyChallengeResponse{
		SessionToken:        result.Token,
		ExpirationTimestamp: result.ExpirationTimestamp,
	}, nil
}

func (s *AuthnServer) computeChallengeHmac(challengeBytes []byte) []byte {
	h := hmac.New(sha256.New, s.challengeHmacKey)
	h.Write(challengeBytes)
	return h.Sum(nil)
}

func (s *AuthnServer) validateChallenge(ctx context.Context, challenge *pb.Challenge, req *pb.VerifyChallengeRequest) error {
	if challenge.Version != currentChallengeVersion {
		return sparkerrors.InvalidUserInputErrorf("%w: got version %d, want version %d",
			ErrUnsupportedChallengeVersion,
			challenge.Version,
			currentChallengeVersion)
	}

	if req.ProtectedChallenge.Version != currentProtectionVersion {
		return sparkerrors.InvalidUserInputErrorf("%w: got version %d, want version %d",
			ErrUnsupportedChallengeProtectionVersion,
			req.ProtectedChallenge.Version,
			currentProtectionVersion)
	}

	challengeAge := s.clock.Now().Unix() + clockSkewSeconds - challenge.Timestamp
	if challengeAge > int64(s.config.ChallengeTimeout.Seconds()) {
		return sparkerrors.InvalidUserInputErrorf("%w: challenge expired %d seconds ago",
			ErrChallengeExpired,
			challengeAge-int64(s.config.ChallengeTimeout.Seconds()))
	}

	if !bytes.Equal(req.PublicKey, challenge.PublicKey) {
		return sparkerrors.InvalidUserInputErrorf("%w: request public key does not match challenge public key",
			ErrPublicKeyMismatch)
	}

	if err := s.nonceCache.markNonceUsed(ctx, challenge.Nonce); err != nil {
		if errors.Is(err, ErrChallengeReused) {
			return sparkerrors.InvalidUserInputErrorf("challenge reused: %w", err)
		}
		return fmt.Errorf("failed to mark nonce as used: %w", err)
	}

	return nil
}

func (s *AuthnServer) verifyClientSignature(challengeBytes []byte, pubKey keys.Public, signature []byte) error {
	if len(challengeBytes) == 0 {
		return sparkerrors.InvalidUserInputErrorf("invalid input: challenge bytes cannot be empty")
	}

	hash := sha256.Sum256(challengeBytes)
	if err := common.VerifyECDSASignature(pubKey.Serialize(), signature, hash[:]); err != nil {
		return sparkerrors.InvalidUserInputErrorf("%w: %w", ErrInvalidSignature, err)
	}

	return nil
}

func (s *AuthnServer) verifyChallengeHmac(challengeBytes []byte, serverHMAC []byte) error {
	expectedMAC := s.computeChallengeHmac(challengeBytes)
	if !hmac.Equal(expectedMAC, serverHMAC) {
		return sparkerrors.InvalidUserInputErrorf("verify challenge hmac: %w", ErrInvalidChallengeHmac)
	}
	return nil
}
