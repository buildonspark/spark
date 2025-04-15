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

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	pb "github.com/lightsparkdev/spark-go/proto/spark_authn"
	"github.com/lightsparkdev/spark-go/so/authninternal"
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
func (cnc *challengeNonceCache) markNonceUsed(nonce []byte) error {
	// Add is atomic - returns nil only if the key didn't exist and was added
	err := cnc.cache.Add(string(nonce), true, cache.DefaultExpiration)
	if err != nil {
		return ErrChallengeReused
	}
	return nil
}

// generateNonce generates a new nonce for use in a challenge
func (cnc *challengeNonceCache) generateNonce() ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate secure nonce: %v", err)
	}
	return nonce, nil
}

// AuthnServerConfig contains the configuration for the AuthenticationServer
type AuthnServerConfig struct {
	// Server's secp256k1 private key for identity
	IdentityPrivateKey []byte
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

	if len(config.IdentityPrivateKey) == 0 {
		return nil, fmt.Errorf("%w: identity private key is required", ErrInternalError)
	}

	if config.ChallengeTimeout < time.Second {
		return nil, fmt.Errorf("%w: challenge timeout must be at least one second", ErrInternalError)
	}

	// Derive challenge HMAC key from identity key and constant
	h := sha256.New()
	h.Write(config.IdentityPrivateKey)
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
		return nil, fmt.Errorf("invalid request: request cannot be nil")
	}

	if len(req.PublicKey) == 0 {
		return nil, fmt.Errorf("%w: public key cannot be empty", ErrInvalidPublicKeyFormat)
	}

	nonce, err := s.nonceCache.generateNonce()
	if err != nil {
		return nil, fmt.Errorf("internal error: failed to generate nonce: %v", err)
	}

	_, err = secp256k1.ParsePubKey(req.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid secp256k1 public key format: %v", ErrInvalidPublicKeyFormat, err)
	}

	challenge := &pb.Challenge{
		Version:   currentChallengeVersion,
		Timestamp: s.clock.Now().Unix(),
		Nonce:     nonce,
		PublicKey: req.PublicKey,
	}

	challengeBytes, err := proto.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("internal error: failed to serialize challenge: %v", err)
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
func (s *AuthnServer) VerifyChallenge(_ context.Context, req *pb.VerifyChallengeRequest) (*pb.VerifyChallengeResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("invalid request: request cannot be nil")
	}

	if req.ProtectedChallenge == nil {
		return nil, fmt.Errorf("invalid request: protected challenge cannot be nil")
	}

	if req.ProtectedChallenge.Challenge == nil {
		return nil, fmt.Errorf("invalid request: challenge cannot be nil")
	}

	if len(req.Signature) == 0 {
		return nil, fmt.Errorf("invalid request: signature cannot be empty")
	}

	if len(req.PublicKey) == 0 {
		return nil, fmt.Errorf("invalid request: public key cannot be empty")
	}

	challenge := req.ProtectedChallenge.Challenge

	if err := s.validateChallenge(challenge, req); err != nil {
		return nil, fmt.Errorf("challenge validation failed: %w", err)
	}

	challengeBytes, err := proto.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("internal error: failed to serialize challenge: %w", err)
	}

	if err := s.verifyChallengeHmac(challengeBytes, req.ProtectedChallenge.ServerHmac); err != nil {
		return nil, fmt.Errorf("challenge verification failed: %w", err)
	}

	if err := s.verifyClientSignature(challengeBytes, req.PublicKey, req.Signature); err != nil {
		return nil, fmt.Errorf("signature verification failed: %w", err)
	}

	result, err := s.sessionTokenCreatorVerifier.CreateToken(req.PublicKey, s.config.SessionDuration)
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

func (s *AuthnServer) validateChallenge(challenge *pb.Challenge, req *pb.VerifyChallengeRequest) error {
	if challenge.Version != currentChallengeVersion {
		return fmt.Errorf("%w: got version %d, want version %d",
			ErrUnsupportedChallengeVersion,
			challenge.Version,
			currentChallengeVersion)
	}

	if req.ProtectedChallenge.Version != currentProtectionVersion {
		return fmt.Errorf("%w: got version %d, want version %d",
			ErrUnsupportedChallengeProtectionVersion,
			req.ProtectedChallenge.Version,
			currentProtectionVersion)
	}

	challengeAge := s.clock.Now().Unix() + clockSkewSeconds - challenge.Timestamp
	if challengeAge > int64(s.config.ChallengeTimeout.Seconds()) {
		return fmt.Errorf("%w: challenge expired %d seconds ago",
			ErrChallengeExpired,
			challengeAge-int64(s.config.ChallengeTimeout.Seconds()))
	}

	if !bytes.Equal(req.PublicKey, challenge.PublicKey) {
		return fmt.Errorf("%w: request public key does not match challenge public key",
			ErrPublicKeyMismatch)
	}

	if err := s.nonceCache.markNonceUsed(challenge.Nonce); err != nil {
		return fmt.Errorf("failed to mark nonce as used: %w", err)
	}

	return nil
}

func (s *AuthnServer) verifyClientSignature(challengeBytes []byte, pubKeyBytes []byte, signature []byte) error {
	if len(challengeBytes) == 0 {
		return fmt.Errorf("invalid input: challenge bytes cannot be empty")
	}

	if len(pubKeyBytes) == 0 {
		return fmt.Errorf("invalid input: public key bytes cannot be empty")
	}

	if len(signature) == 0 {
		return fmt.Errorf("invalid input: signature cannot be empty")
	}

	pubKey, err := secp256k1.ParsePubKey(pubKeyBytes)
	if err != nil {
		return fmt.Errorf("%w: failed to parse public key: %v", ErrInvalidPublicKeyFormat, err)
	}

	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return fmt.Errorf("invalid signature format: malformed DER signature: %v", err)
	}

	hash := sha256.Sum256(challengeBytes)
	if !sig.Verify(hash[:], pubKey) {
		return fmt.Errorf("%w: signature verification failed", ErrInvalidSignature)
	}

	return nil
}

func (s *AuthnServer) verifyChallengeHmac(challengeBytes []byte, serverHMAC []byte) error {
	expectedMAC := s.computeChallengeHmac(challengeBytes)
	if !hmac.Equal(expectedMAC, serverHMAC) {
		return ErrInvalidChallengeHmac
	}
	return nil
}
