package authn

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/authninternal"
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

type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// AuthnInterceptor is an interceptor that validates session tokens and adds session info to the context.
// If there is no session or it does not validate, it will log rather than error.
func (i *AuthnInterceptor) AuthnInterceptor(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	ctx = i.authenticateContext(ctx)
	return handler(ctx, req)
}

func (i *AuthnInterceptor) StreamAuthnInterceptor(srv interface{}, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	newCtx := i.authenticateContext(ss.Context())
	return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: newCtx})
}

func (i *AuthnInterceptor) authenticateContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	logger := logging.GetLoggerFromContext(ctx)
	if !ok {
		err := fmt.Errorf("no metadata provided")
		logger.Info("Authentication error", "error", err)
		return context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: err,
		})

	}

	// Tokens are typically sent in "authorization" header
	tokens := md.Get(authorizationHeader)
	if len(tokens) == 0 {
		err := fmt.Errorf("no authorization token provided")
		return context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: err,
		})
	}

	// Usually follows "Bearer <token>" format
	token := strings.TrimPrefix(tokens[0], "Bearer ")

	sessionInfo, err := i.sessionTokenCreatorVerifier.VerifyToken(token)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to verify token: %w", err)
		logger.Info("Authentication error", "error", wrappedErr)
		return context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: wrappedErr,
		})
	}

	key, err := secp256k1.ParsePubKey(sessionInfo.PublicKey)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to parse public key: %w", err)
		logger.Info("Authentication error", "error", wrappedErr)
		return context.WithValue(ctx, authnContextKey, &AuthnContext{
			Error: wrappedErr,
		})
	}

	return context.WithValue(ctx, authnContextKey, &AuthnContext{
		Session: &Session{
			identityPublicKey:      key,
			identityPublicKeyBytes: sessionInfo.PublicKey,
			expirationTimestamp:    sessionInfo.ExpirationTimestamp,
		},
	})
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
