package authn

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/so/authninternal"
	"github.com/lightsparkdev/spark-go/so/helper"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	authnContextKey     = contextKey("authn_context")
	authorizationHeader = "authorization"
)

// AuthnContext holds authentication information including the session and any error
type AuthnContext struct { //nolint:revive
	Session *Session
	Error   error
}

// Session represents the session information to be used within the product.
type Session struct {
	identityPublicKey      *secp256k1.PublicKey
	identityPublicKeyBytes []byte
	expirationTimestamp    int64
}

// IdentityPublicKey returns the public key
func (s *Session) IdentityPublicKey() *secp256k1.PublicKey {
	return s.identityPublicKey
}

// IdentityPublicKeyBytes returns the public key bytes
func (s *Session) IdentityPublicKeyBytes() []byte {
	return s.identityPublicKeyBytes
}

// ExpirationTimestamp returns the expiration of the session
func (s *Session) ExpirationTimestamp() int64 {
	return s.expirationTimestamp
}

// AuthnInterceptor is an interceptor that validates session tokens and adds session info to the context.
type AuthnInterceptor struct { //nolint:revive
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier
}

// NewAuthnInterceptor creates a new AuthnInterceptor
func NewAuthnInterceptor(sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier) *AuthnInterceptor {
	return &AuthnInterceptor{
		sessionTokenCreatorVerifier: sessionTokenCreatorVerifier,
	}
}

// AuthnInterceptor is an interceptor that validates session tokens and adds session info to the context.
// If there is no session or it does not validate, it will log rather than error.
func (i *AuthnInterceptor) AuthnInterceptor(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	logger := helper.GetLoggerFromContext(ctx)
	if !ok {
		err := fmt.Errorf("no metadata provided")
		logger.Info("Authentication error", "error", err)
		ctx = context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: err,
		})
		return handler(ctx, req)
	}

	// Tokens are typically sent in "authorization" header
	tokens := md.Get(authorizationHeader)
	if len(tokens) == 0 {
		err := fmt.Errorf("no authorization token provided")
		ctx = context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: err,
		})
		return handler(ctx, req)
	}

	// Usually follows "Bearer <token>" format
	token := strings.TrimPrefix(tokens[0], "Bearer ")

	sessionInfo, err := i.sessionTokenCreatorVerifier.VerifyToken(token)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to verify token: %w", err)
		logger.Info("Authentication error", "error", wrappedErr)
		ctx = context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: wrappedErr,
		})
		return handler(ctx, req)
	}

	key, err := secp256k1.ParsePubKey(sessionInfo.PublicKey)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to parse public key: %w", err)
		logger.Info("Authentication error", "error", wrappedErr)
		ctx = context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: wrappedErr,
		})
		return handler(ctx, req)
	}

	ctx = context.WithValue(ctx, authnContextKey, &AuthnContext{
		Session: &Session{
			identityPublicKey:      key,
			identityPublicKeyBytes: sessionInfo.PublicKey,
			expirationTimestamp:    sessionInfo.ExpirationTimestamp,
		},
	})

	return handler(ctx, req)
}

// GetSessionFromContext retrieves the session and any error from the context
func GetSessionFromContext(ctx context.Context) (*Session, error) {
	val := ctx.Value(authnContextKey)
	if val == nil {
		return nil, fmt.Errorf("no authentication context in context")
	}

	authnCtx, ok := val.(*AuthnContext)
	if !ok {
		return nil, fmt.Errorf("invalid authentication context type")
	}

	if authnCtx.Error != nil {
		return nil, authnCtx.Error
	}

	return authnCtx.Session, nil
}
