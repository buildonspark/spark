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
	"github.com/lightsparkdev/spark/so/rpcpolicy"
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

// Interceptor validates session tokens and adds session info to the context.
type Interceptor struct {
	sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier
	// isAuthenticated returns true for methods that require a session token. In production this is wired to
	// rpcpolicy.IsAuthenticated; tests can override via NewInterceptorWithAuthenticatedFunc.
	isAuthenticated func(fullMethod string) bool
}

// NewInterceptor creates a new Interceptor backed by the declarative rpcpolicy table.
func NewInterceptor(sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier) *Interceptor {
	return &Interceptor{
		sessionTokenCreatorVerifier: sessionTokenCreatorVerifier,
		isAuthenticated:             rpcpolicy.IsAuthenticated,
	}
}

// NewInterceptorWithAuthenticatedFunc creates an interceptor with a custom "is this method authenticated" predicate.
// Intended for tests that want to isolate the interceptor from the global policy table.
func NewInterceptorWithAuthenticatedFunc(sessionTokenCreatorVerifier *authninternal.SessionTokenCreatorVerifier, isAuthenticated func(fullMethod string) bool) *Interceptor {
	return &Interceptor{
		sessionTokenCreatorVerifier: sessionTokenCreatorVerifier,
		isAuthenticated:             isAuthenticated,
	}
}

type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// AuthnInterceptor validates session tokens and adds session info to the context.
// Unauthenticated requests are rejected unless the method is explicitly marked as unauthenticated.
// For unauthenticated methods, we still attempt to extract the session if a token is present.
func (i *Interceptor) AuthnInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	requireAuth := i.isAuthenticated(info.FullMethod)
	ctx, err := i.authenticateContext(ctx, requireAuth)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// StreamAuthnInterceptor validates session tokens for streaming RPCs.
// Unauthenticated requests are rejected unless the method is explicitly marked as unauthenticated.
// For unauthenticated methods, we still attempt to extract the session if a token is present.
func (i *Interceptor) StreamAuthnInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	requireAuth := i.isAuthenticated(info.FullMethod)
	ctx, err := i.authenticateContext(ss.Context(), requireAuth)
	if err != nil {
		return err
	}
	ss = &wrappedServerStream{ServerStream: ss, ctx: ctx}
	return handler(srv, ss)
}

func (i *Interceptor) authenticateContext(ctx context.Context, requireAuth bool) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	logger := logging.GetLoggerFromContext(ctx)
	if !ok {
		if requireAuth {
			err := errors.WrapErrorWithCode(fmt.Errorf("no metadata provided"), codes.Unauthenticated)
			logger.Info("Authentication error", zap.Error(err))
			return nil, err
		}
		return ctx, nil
	}

	tokens := md.Get(authorizationHeader)
	if len(tokens) == 0 {
		if requireAuth {
			return nil, errors.WrapErrorWithCode(fmt.Errorf("no authorization token provided"), codes.Unauthenticated)
		}
		return ctx, nil
	}

	token := strings.TrimPrefix(tokens[0], "Bearer ")

	sessionInfo, err := i.sessionTokenCreatorVerifier.VerifyToken(token)
	if err != nil {
		if requireAuth {
			return nil, errors.WrapErrorWithCode(fmt.Errorf("failed to verify token: %w", err), codes.Unauthenticated)
		}
		return ctx, nil
	}

	key, err := keys.ParsePublicKey(sessionInfo.GetPublicKey())
	if err != nil {
		if requireAuth {
			return nil, errors.WrapErrorWithCode(fmt.Errorf("failed to parse public key: %w", err), codes.Unauthenticated)
		}
		return ctx, nil
	}

	ctx, _ = logging.WithIdentityPubkey(ctx, key)

	return context.WithValue(ctx, authnContextKey, &Context{
		Session: &Session{
			identityPublicKey:   key,
			expirationTimestamp: sessionInfo.GetExpirationTimestamp(),
		},
	}), nil
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
