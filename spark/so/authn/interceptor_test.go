package authn

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/authninternal"
)

func testConfig() UnauthenticatedConfig {
	return UnauthenticatedConfig{
		Methods:         map[string]struct{}{"/test.Service/Unauthenticated": {}},
		ServicePrefixes: []string{"/internal.Service/"},
	}
}

func TestUnauthenticatedConfig(t *testing.T) {
	config := testConfig()

	assert.True(t, config.IsUnauthenticated("/test.Service/Unauthenticated"))
	assert.True(t, config.IsUnauthenticated("/internal.Service/AnyMethod"))
	assert.False(t, config.IsUnauthenticated("/public.Service/Method"))
}

func TestAuthnInterceptor_SkipsUnauthenticatedMethod(t *testing.T) {
	interceptor := NewInterceptorWithConfig(nil, testConfig())

	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "response", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Unauthenticated"}
	resp, err := interceptor.AuthnInterceptor(t.Context(), nil, info, handler)

	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, "response", resp)
}

func TestAuthnInterceptor_RejectsWithoutToken(t *testing.T) {
	interceptor := NewInterceptorWithConfig(&authninternal.SessionTokenCreatorVerifier{}, testConfig())

	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "response", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/public.Service/Method"}
	_, err := interceptor.AuthnInterceptor(t.Context(), nil, info, handler)

	require.Error(t, err)
	assert.False(t, handlerCalled)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestAuthnInterceptor_AcceptsValidToken(t *testing.T) {
	identityKey := keys.GeneratePrivateKey()
	tokenVerifier, err := authninternal.NewSessionTokenCreatorVerifier(identityKey, authninternal.RealClock{})
	require.NoError(t, err)

	userKey := keys.GeneratePrivateKey()
	tokenResult, err := tokenVerifier.CreateToken(userKey.Public(), time.Hour)
	require.NoError(t, err)

	interceptor := NewInterceptorWithConfig(tokenVerifier, testConfig())

	handlerCalled := false
	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		capturedCtx = ctx
		return "response", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/public.Service/Method"}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+tokenResult.Token))
	resp, err := interceptor.AuthnInterceptor(ctx, nil, info, handler)

	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, "response", resp)

	session, err := GetSessionFromContext(capturedCtx)
	require.NoError(t, err)
	assert.True(t, session.IdentityPublicKey().Equals(userKey.Public()))
}

func TestAuthnInterceptor_RejectsInvalidToken(t *testing.T) {
	identityKey := keys.GeneratePrivateKey()
	tokenVerifier, err := authninternal.NewSessionTokenCreatorVerifier(identityKey, authninternal.RealClock{})
	require.NoError(t, err)

	interceptor := NewInterceptorWithConfig(tokenVerifier, testConfig())

	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "response", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/public.Service/Method"}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer invalid-token"))
	_, err = interceptor.AuthnInterceptor(ctx, nil, info, handler)

	require.Error(t, err)
	assert.False(t, handlerCalled)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestStreamAuthnInterceptor_SkipsUnauthenticatedMethod(t *testing.T) {
	interceptor := NewInterceptorWithConfig(nil, testConfig())

	handlerCalled := false
	handler := func(srv any, ss grpc.ServerStream) error {
		handlerCalled = true
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Unauthenticated"}
	err := interceptor.StreamAuthnInterceptor(nil, &mockServerStream{ctx: t.Context()}, info, handler)

	require.NoError(t, err)
	assert.True(t, handlerCalled)
}

func TestStreamAuthnInterceptor_RejectsWithoutToken(t *testing.T) {
	interceptor := NewInterceptorWithConfig(&authninternal.SessionTokenCreatorVerifier{}, testConfig())

	handlerCalled := false
	handler := func(srv any, ss grpc.ServerStream) error {
		handlerCalled = true
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/public.Service/Method"}
	err := interceptor.StreamAuthnInterceptor(nil, &mockServerStream{ctx: t.Context()}, info, handler)

	require.Error(t, err)
	assert.False(t, handlerCalled)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}
