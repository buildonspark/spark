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
	"google.golang.org/protobuf/proto"
)

const (
	currentChallengeVersion  = 1
	currentProtectionVersion = 1
	challengeSecretConstant  = "AUTH_CHALLENGE_SECRET_v1"
)

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
}

var (
	// ErrUnsupportedChallengeVersion is returned when the challenge version is unsupported.
	ErrUnsupportedChallengeVersion = errors.New("unsupported challenge version")
	// ErrUnsupportedChallengeProtectionVersion is returned when the challenge protection version is unsupported.
	ErrUnsupportedChallengeProtectionVersion = errors.New("unsupported challenge protection version")
	// ErrChallengeExpired is returned when the challenge has expired.
	ErrChallengeExpired = errors.New("challenge expired")
	// ErrPublicKeyMismatch is returned when the public key does not match the challenge.
	ErrPublicKeyMismatch = errors.New("public key does not match challenge")
	// ErrInvalidChallengeHmac is returned when the challenge hmac is invalid.
	ErrInvalidChallengeHmac = errors.New("invalid challenge hmac")
	// ErrInvalidPublicKeyFormat is returned when the public key format is invalid.
	ErrInvalidPublicKeyFormat = errors.New("invalid public key format")
	// ErrInvalidSignature is returned when the client signature is invalid.
	ErrInvalidSignature = errors.New("invalid client signature")
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
		return nil, errors.New("identity private key is required")
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
	}, nil
}

// GetChallenge generates a new challenge for the given public key.
// This is the first step of the authentication process.
func (s *AuthnServer) GetChallenge(ctx context.Context, req *pb.GetChallengeRequest) (*pb.GetChallengeResponse, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %v", err)
	}

	_, err := secp256k1.ParsePubKey(req.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPublicKeyFormat, err)
	}

	challenge := &pb.Challenge{
		Version:   currentChallengeVersion,
		Timestamp: s.clock.Now().Unix(),
		Nonce:     nonce,
		PublicKey: req.PublicKey,
	}

	challengeBytes, err := proto.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize challenge: %v", err)
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
	challenge := req.ProtectedChallenge.Challenge

	if err := s.validateChallenge(challenge, req); err != nil {
		return nil, err
	}

	challengeBytes, err := proto.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize challenge: %w", err)
	}

	if err := s.verifyChallengeHmac(challengeBytes, req.ProtectedChallenge.ServerHmac); err != nil {
		return nil, err
	}

	if err := s.verifyClientSignature(challengeBytes, req.PublicKey, req.Signature); err != nil {
		return nil, err
	}

	result, err := s.sessionTokenCreatorVerifier.CreateToken(req.PublicKey, s.config.SessionDuration)
	if err != nil {
		return nil, fmt.Errorf("failed to create session token: %w", err)
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
		return fmt.Errorf("%w: %d", ErrUnsupportedChallengeVersion, challenge.Version)
	}

	if req.ProtectedChallenge.Version != currentProtectionVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedChallengeProtectionVersion, req.ProtectedChallenge.Version)
	}

	if s.clock.Now().Unix()-challenge.Timestamp > int64(s.config.ChallengeTimeout.Seconds()) {
		return ErrChallengeExpired
	}

	if !bytes.Equal(req.PublicKey, challenge.PublicKey) {
		return ErrPublicKeyMismatch
	}

	return nil
}

func (s *AuthnServer) verifyClientSignature(challengeBytes []byte, pubKeyBytes []byte, signature []byte) error {
	pubKey, err := secp256k1.ParsePubKey(pubKeyBytes)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return fmt.Errorf("invalid signature format: %w", err)
	}

	hash := sha256.Sum256(challengeBytes)
	if !sig.Verify(hash[:], pubKey) {
		return ErrInvalidSignature
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
