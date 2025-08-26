package authninternal

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	pb "github.com/lightsparkdev/spark/proto/spark_authn_internal"
	"google.golang.org/protobuf/proto"
)

const (
	sessionSecretConstant           = "AUTH_SESSION_SECRET_v1"
	currentSessionProtectionVersion = 1
	currentSessionVersion           = 1
)

var (
	// ErrInvalidIdentityKey is returned when the identity private key is invalid.
	ErrInvalidIdentityKey = fmt.Errorf("identity private key is required")

	// ErrInvalidTokenEncoding is returned when the token encoding is invalid.
	ErrInvalidTokenEncoding = fmt.Errorf("invalid token encoding")

	// ErrUnsupportedProtectionVersion is returned when the session protection version is unsupported.
	ErrUnsupportedProtectionVersion = fmt.Errorf("unsupported session protection version")

	// ErrUnsupportedSessionVersion is returned when the session version is unsupported.
	ErrUnsupportedSessionVersion = fmt.Errorf("unsupported session version")

	// ErrInvalidTokenHmac is returned when the session token hmac is invalid.
	ErrInvalidTokenHmac = fmt.Errorf("invalid session token hmac")

	// ErrTokenExpired is returned when the token has expired.
	ErrTokenExpired = fmt.Errorf("token has expired")
)

// TokenCreationResult contains the token and its associated metadata
type TokenCreationResult struct {
	Token               string
	ExpirationTimestamp int64
}

// SessionTokenCreatorVerifier handles creation and verification of session tokens
type SessionTokenCreatorVerifier struct {
	sessionHmacKey []byte
	clock          Clock
}

// NewSessionTokenCreatorVerifier creates a new SessionTokenCreatorVerifier.
// If the clock is nil, it will use the real clock.
func NewSessionTokenCreatorVerifier(identityPrivateKey keys.Private, clock Clock) (*SessionTokenCreatorVerifier, error) {
	if clock == nil {
		clock = RealClock{}
	}

	if identityPrivateKey.IsZero() {
		return nil, ErrInvalidIdentityKey
	}

	// Derive session HMAC key from identity key
	h := sha256.New()
	h.Write(identityPrivateKey.Serialize())
	h.Write([]byte(sessionSecretConstant))
	sessionHmacKey := h.Sum(nil)

	return &SessionTokenCreatorVerifier{
		sessionHmacKey: sessionHmacKey,
		clock:          clock,
	}, nil
}

func (stcv *SessionTokenCreatorVerifier) computeSessionHmac(sessionBytes []byte) []byte {
	h := hmac.New(sha256.New, stcv.sessionHmacKey)
	h.Write(sessionBytes)
	return h.Sum(nil)
}

// CreateToken generates a new session token and returns both the token and its expiration time
func (stcv *SessionTokenCreatorVerifier) CreateToken(publicKey keys.Public, duration time.Duration) (*TokenCreationResult, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	expirationTimestamp := stcv.clock.Now().Add(duration).Unix()

	session := &pb.Session{
		Version:             currentSessionVersion,
		ExpirationTimestamp: expirationTimestamp,
		Nonce:               nonce,
		PublicKey:           publicKey.Serialize(),
	}

	sessionBytes, err := proto.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize session: %w", err)
	}

	mac := stcv.computeSessionHmac(sessionBytes)

	protected := &pb.ProtectedSession{
		Version: currentSessionProtectionVersion,
		Session: session,
		Hmac:    mac,
	}

	protectedBytes, err := proto.Marshal(protected)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize protected session: %w", err)
	}

	token := base64.URLEncoding.EncodeToString(protectedBytes)

	return &TokenCreationResult{
		Token:               token,
		ExpirationTimestamp: expirationTimestamp,
	}, nil
}

// VerifyToken validates a session token and returns the session data
func (stcv *SessionTokenCreatorVerifier) VerifyToken(token string) (*pb.Session, error) {
	protectedBytes, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTokenEncoding, err)
	}

	protected := &pb.ProtectedSession{}
	if err := proto.Unmarshal(protectedBytes, protected); err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	if protected.Version != currentSessionProtectionVersion {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedProtectionVersion, protected.Version)
	}
	if protected.Session == nil {
		return nil, fmt.Errorf("invalid token: missing session")
	}
	if protected.Session.Version != currentSessionVersion {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSessionVersion, protected.Session.Version)
	}

	sessionBytes, err := proto.Marshal(protected.Session)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize session: %w", err)
	}

	expectedMAC := stcv.computeSessionHmac(sessionBytes)
	if !hmac.Equal(expectedMAC, protected.Hmac) {
		return nil, ErrInvalidTokenHmac
	}

	if stcv.clock.Now().Unix() > protected.Session.ExpirationTimestamp {
		return nil, ErrTokenExpired
	}

	return protected.Session, nil
}
