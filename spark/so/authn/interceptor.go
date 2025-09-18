package authn

import (
	"context"
	"fmt"
	"strings"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/authninternal"
	"github.com/lightsparkdev/spark/so/errors"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	authnContextKey     = contextKey("authn_context")
	authorizationHeader = "authorization"
)

// Context holds authentication information including the session and any error
type Context struct {
	Session *Session
	Error   error
}

// Session represents the session information to be used within the product.
type Session struct {
	identityPublicKey   keys.Public
	expirationTimestamp int64
}

// IdentityPublicKey returns the public key
func (s *Session) IdentityPublicKey() keys.Public {
	return s.identityPublicKey
}

// ExpirationTimestamp returns the expiration of the session
func (s *Session) ExpirationTimestamp() int64 {
	return s.expirationTimestamp
}

// Interceptor is an interceptor that validates session tokens and adds session info to the context.
type Interceptor struct {
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier
}

// NewInterceptor creates a new Interceptor
func NewInterceptor(sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier) *Interceptor {
	return &Interceptor{
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
// If there is no session, or it does not validate, it will log rather than error.
func (i *Interceptor) AuthnInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx = i.authenticateContext(ctx)
	return handler(ctx, req)
}

func (i *Interceptor) StreamAuthnInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	newCtx := i.authenticateContext(ss.Context())
	return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: newCtx})
}

func (i *Interceptor) authenticateContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	logger := logging.GetLoggerFromContext(ctx)
	if !ok {
		err := errors.WrapErrorWithCode(fmt.Errorf("no metadata provided"), codes.Unauthenticated)
		logger.Info("Authentication error", zap.Error(err))
		return context.WithValue(ctx, authnContextKey, &Context{
			Error: err,
		})

	}

	// Tokens are typically sent in "authorization" header
	tokens := md.Get(authorizationHeader)
	if len(tokens) == 0 {
		err := errors.WrapErrorWithCode(fmt.Errorf("no authorization token provided"), codes.Unauthenticated)
		return context.WithValue(ctx, authnContextKey, &Context{
			Error: err,
		})
	}

	// Usually follows "Bearer <token>" format
	token := strings.TrimPrefix(tokens[0], "Bearer ")

	sessionInfo, err := i.sessionTokenCreatorVerifier.VerifyToken(token)
	if err != nil {
		wrappedErr := errors.WrapErrorWithCode(fmt.Errorf("failed to verify token: %w", err), codes.Unauthenticated)
		return context.WithValue(ctx, authnContextKey, &Context{
			Error: wrappedErr,
		})
	}

	key, err := keys.ParsePublicKey(sessionInfo.PublicKey)
	if err != nil {
		wrappedErr := errors.WrapErrorWithCode(fmt.Errorf("failed to parse public key: %w", err), codes.Unauthenticated)
		return context.WithValue(ctx, authnContextKey, &Context{
			Error: wrappedErr,
		})
	}

	ctx, logger = logging.WithIdentityPubkey(ctx, key)

	return context.WithValue(ctx, authnContextKey, &Context{
		Session: &Session{
			identityPublicKey:   key,
			expirationTimestamp: sessionInfo.ExpirationTimestamp,
		},
	})
}

// GetSessionFromContext retrieves the session and any error from the context
func GetSessionFromContext(ctx context.Context) (*Session, error) {
	val := ctx.Value(authnContextKey)
	if val == nil {
		return nil, fmt.Errorf("no authentication context in context")
	}

	authnCtx, ok := val.(*Context)
	if !ok {
		return nil, fmt.Errorf("invalid authentication context type")
	}

	if authnCtx.Error != nil {
		return nil, authnCtx.Error
	}

	return authnCtx.Session, nil
}
