package authz

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const (
	ProtectedTestService   = "test.ProtectedGrpcService"
	UnprotectedTestService = "test.UnprotectedService"

	ProtectedTestMethod   = "/" + ProtectedTestService + "/SomeMethod"
	UnprotectedTestMethod = "/" + UnprotectedTestService + "/SomeMethod"
	OtherServiceMethod    = "/test.OtherGrpcService/SomeMethod"

	LoadBalancerIP = "192.168.200.200"

	TestIPAllowed1   = "192.168.1.1"
	TestIPAllowed2   = "9.9.9.9"
	TestIPDisallowed = "172.16.1.1"

	TestIPBadFormat = "192.168.1.1:12345"
)

func unaryHandler(_ context.Context, _ any) (any, error) {
	return nil, nil
}

func streamHandler(_ any, _ grpc.ServerStream) error {
	return nil
}

type contextToUse int

const (
	ContextToUseNone contextToUse = iota
	ContextToUseMetadata
	ContextToUsePeer
)

func TestAuthzInterceptor(t *testing.T) {
	tests := []struct {
		name               string
		config             *InterceptorConfig
		fullMethod         string
		forwardedFromAddrs []string
		peerAddr           string
		contextToUse       contextToUse
		expectError        bool
		expectedCode       codes.Code
		expectedMsg        string
	}{
		{
			name:               "disabled authorization allows all requests",
			config:             NewAuthzConfig(),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "warn allows all requests",
			config: &InterceptorConfig{
				AllowedIPs:        []string{},
				Mode:              ModeWarn,
				ProtectedServices: []string{},
			},
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "enforce with empty allowlist disallows requests with x-forwarded-for header",
			config: &InterceptorConfig{
				AllowedIPs:        []string{},
				Mode:              ModeEnforce,
				ProtectedServices: []string{},
			},
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
		},
		{
			name:               "allowed IP passes authorization",
			config:             NewAuthzConfig(WithMode(ModeEnforce), WithAllowedIPs([]string{TestIPAllowed1, TestIPAllowed2})),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name:               "disallowed IP is rejected",
			config:             NewAuthzConfig(WithMode(ModeEnforce), WithAllowedIPs([]string{TestIPAllowed1, TestIPAllowed2})),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
			expectedCode:       codes.PermissionDenied,
			expectedMsg:        "request not allowed from " + TestIPDisallowed,
		},
		{
			name:         "missing peer context returns error",
			config:       NewAuthzConfig(WithMode(ModeEnforce), WithAllowedIPs([]string{TestIPAllowed1})),
			fullMethod:   ProtectedTestMethod,
			contextToUse: ContextToUseNone,
			expectError:  true,
			expectedCode: codes.Internal,
			expectedMsg:  "failed to get peer information",
		},
		{
			name: "protected services only - allowed service requires authorization",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "protected services only - non-protected service bypasses authorization",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         UnprotectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "protected services only - disallowed IP on protected service is rejected",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
			expectedCode:       codes.PermissionDenied,
			expectedMsg:        "request not allowed from " + TestIPDisallowed,
		},
		{
			name: "protected services only - method with similar prefix but not exact service is not protected",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         OtherServiceMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "protected services only - method with service as a substring is not protected",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         "/foo/test.ProtectedGrpcService/SomeMethodBar/SomeMethod",
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "invalid IP in allowlist doesn't allow request",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPBadFormat}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
		},
		{
			name: "protected services only - service is protected when peer context is used as backup",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUsePeer,
			expectError:        true,
		},
		{
			name:               "log only mode allows all requests regardless of IP",
			config:             NewAuthzConfig(WithMode(ModeLogOnly), WithAllowedIPs([]string{TestIPAllowed1})),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name:               "log only mode allows requests from non-allowlisted IPs",
			config:             NewAuthzConfig(WithMode(ModeLogOnly)),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name:               "log only mode allows requests from allowlisted IPs",
			config:             NewAuthzConfig(WithMode(ModeLogOnly), WithAllowedIPs([]string{TestIPAllowed1, TestIPAllowed2})),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "log only mode with protected services allows all requests to protected services",
			config: NewAuthzConfig(
				WithMode(ModeLogOnly),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "log only mode with protected services allows all requests to unprotected services",
			config: NewAuthzConfig(
				WithMode(ModeLogOnly),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
			),
			fullMethod:         UnprotectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name:               "x-forwarded-for with multiple IPs uses last IP",
			config:             NewAuthzConfig(WithMode(ModeEnforce), WithAllowedIPs([]string{TestIPAllowed1})),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{"192.168.1.100", "192.168.1.200", TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name:               "x-forwarded-for with multiple IPs rejects if last IP not allowed",
			config:             NewAuthzConfig(WithMode(ModeEnforce), WithAllowedIPs([]string{TestIPAllowed1})),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1, "192.168.1.200", TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
			expectedCode:       codes.PermissionDenied,
			expectedMsg:        "request not allowed from " + TestIPDisallowed,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 1 uses second-to-last IP",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(1),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed, TestIPAllowed1, "192.168.1.200"},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 1 rejects if second-to-last IP not allowed",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(1),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1, TestIPDisallowed, "192.168.1.200"},
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
			expectedCode:       codes.PermissionDenied,
			expectedMsg:        "request not allowed from " + TestIPDisallowed,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 2 uses third-to-last IP",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(2),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1, "192.168.1.200", TestIPDisallowed},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 2 rejects if third-to-last IP not allowed",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(2),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed, "192.168.1.200", TestIPAllowed1},
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
			expectedCode:       codes.PermissionDenied,
			expectedMsg:        "request not allowed from " + TestIPDisallowed,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 1 in log only mode allows all requests",
			config: NewAuthzConfig(
				WithMode(ModeLogOnly),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(1),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed, TestIPAllowed1, "192.168.1.200"},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 1 in warn mode allows all requests",
			config: NewAuthzConfig(
				WithMode(ModeWarn),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(1),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed, TestIPAllowed1, "192.168.1.200"},
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 1 falls back to peer IP when header parsing fails",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(5), // Position beyond available IPs
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPAllowed1, "192.168.1.200"},
			peerAddr:           TestIPDisallowed + ":12345",
			contextToUse:       ContextToUseMetadata,
			expectError:        true,
			expectedCode:       codes.PermissionDenied,
			expectedMsg:        "request not allowed from " + TestIPDisallowed,
		},
		{
			name: "x-forwarded-for with XffClientIpPosition 1 and internal peer IP allows request",
			config: NewAuthzConfig(
				WithMode(ModeEnforce),
				WithAllowedIPs([]string{TestIPAllowed1}),
				WithProtectedServices([]string{ProtectedTestService}),
				WithXffClientIpPosition(1),
			),
			fullMethod:         ProtectedTestMethod,
			forwardedFromAddrs: []string{TestIPDisallowed, TestIPAllowed1, "192.168.1.200"},
			peerAddr:           "10.0.0.1:12345", // Internal VPC IP
			contextToUse:       ContextToUseMetadata,
			expectError:        false,
		},
	}

	for _, tt := range tests {
		for _, interceptorType := range []string{"unary", "stream"} {
			t.Run(tt.name+" ("+interceptorType+")", func(t *testing.T) {
				interceptor := NewAuthzInterceptor(tt.config)

				if len(tt.peerAddr) == 0 {
					tt.peerAddr = "10.0.0.1:12345"
				}

				var ctx context.Context
				switch tt.contextToUse {
				case ContextToUseMetadata:
					ctx = peer.NewContext(metadata.NewIncomingContext(t.Context(), metadata.MD{
						"x-forwarded-for": []string{strings.Join(tt.forwardedFromAddrs, ", ")},
					}), &peer.Peer{Addr: &mockAddr{addr: tt.peerAddr}})
				case ContextToUsePeer:
					peerAddr := ""
					if len(tt.forwardedFromAddrs) > 0 {
						peerAddr = tt.forwardedFromAddrs[0]
					}
					ctx = peer.NewContext(t.Context(), &peer.Peer{Addr: &mockAddr{addr: peerAddr}})
				default:
					ctx = t.Context()
				}

				var err error
				if interceptorType == "unary" {
					info := &grpc.UnaryServerInfo{FullMethod: tt.fullMethod}
					_, err = interceptor.UnaryServerInterceptor(ctx, "request", info, unaryHandler)
				} else {
					info := &grpc.StreamServerInfo{FullMethod: tt.fullMethod}
					err = interceptor.StreamServerInterceptor(nil, &mockServerStream{ctx: ctx}, info, streamHandler)
				}

				if tt.expectError {
					require.Error(t, err)
					if tt.expectedCode != codes.OK {
						statusErr, ok := status.FromError(err)
						require.True(t, ok)
						assert.Equal(t, tt.expectedCode, statusErr.Code())
						if tt.expectedMsg != "" {
							assert.Contains(t, statusErr.Message(), tt.expectedMsg)
						}
					}
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestAuthzConfig(t *testing.T) {
	tests := []struct {
		name               string
		configFn           func() *InterceptorConfig
		expectedAllowedIPs []string
		expectedMode       Mode
		expectedServices   []string
	}{
		{
			name: "DefaultAuthzConfig",
			configFn: func() *InterceptorConfig {
				return NewAuthzConfig()
			},
			expectedAllowedIPs: []string{},
			expectedMode:       ModeDisabled,
			expectedServices:   []string{},
		},
		{
			name: "NewAuthzConfig with IPs",
			configFn: func() *InterceptorConfig {
				return NewAuthzConfig(WithMode(ModeEnforce), WithAllowedIPs([]string{TestIPAllowed1, TestIPAllowed2}))
			},
			expectedAllowedIPs: []string{TestIPAllowed1, TestIPAllowed2},
			expectedMode:       ModeEnforce,
			expectedServices:   []string{},
		},
		{
			name: "NewAuthzConfig with empty IPs",
			configFn: func() *InterceptorConfig {
				return NewAuthzConfig(WithMode(ModeEnforce))
			},
			expectedAllowedIPs: []string{},
			expectedMode:       ModeEnforce,
			expectedServices:   []string{},
		},
		{
			name: "NewAuthzConfigWithProtectedServices",
			configFn: func() *InterceptorConfig {
				return NewAuthzConfig(
					WithMode(ModeEnforce),
					WithAllowedIPs([]string{TestIPAllowed1}),
					WithProtectedServices([]string{ProtectedTestService}),
				)
			},
			expectedAllowedIPs: []string{TestIPAllowed1},
			expectedMode:       ModeEnforce,
			expectedServices:   []string{ProtectedTestService},
		},
		{
			name: "NewAuthzConfig with LogOnly mode",
			configFn: func() *InterceptorConfig {
				return NewAuthzConfig(
					WithMode(ModeLogOnly),
					WithAllowedIPs([]string{TestIPAllowed1, TestIPAllowed2}),
				)
			},
			expectedAllowedIPs: []string{TestIPAllowed1, TestIPAllowed2},
			expectedMode:       ModeLogOnly,
			expectedServices:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := tt.configFn()
			assert.Equal(t, tt.expectedAllowedIPs, config.AllowedIPs)
			assert.Equal(t, tt.expectedMode, config.Mode)
			for i, service := range tt.expectedServices {
				assert.Equal(t, "/"+service+"/", config.ProtectedServices[i])
			}
		})
	}
}

type mockAddr struct {
	addr string
}

func (m *mockAddr) Network() string { return "tcp" }
func (m *mockAddr) String() string  { return m.addr }

type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

// simpleConfig is a simple implementation of the Config interface for testing
type simpleConfig struct {
	authzEnforced bool
}

func (m *simpleConfig) IsAuthzEnforced() bool {
	return m.authzEnforced
}

func TestEnforceSession_PreservesAuthErrorStatus(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})

	tests := []struct {
		name           string
		setupContext   func(context.Context) context.Context
		identityPubKey keys.Public
		config         Config
		expectedCode   codes.Code
		expectedMsg    string
	}{
		{
			name: "NoSession",
			setupContext: func(ctx context.Context) context.Context {
				// Return without adding a session
				return ctx
			},
			identityPubKey: keys.MustGeneratePrivateKeyFromRand(rng).Public(),
			config:         &simpleConfig{authzEnforced: true},
			expectedCode:   codes.Unauthenticated,
			expectedMsg:    "no valid session found",
		},
		{
			name: "IdentityMismatch",
			setupContext: func(ctx context.Context) context.Context {
				// Inject a session where the identity doesn't match
				wrongSessionKey := keys.MustGeneratePrivateKeyFromRand(rng)
				return authn.InjectSessionForTests(ctx, wrongSessionKey.Public().ToHex(), 0)
			},
			identityPubKey: keys.MustGeneratePrivateKeyFromRand(rng).Public(),
			config:         &simpleConfig{authzEnforced: true},
			expectedCode:   codes.PermissionDenied,
			expectedMsg:    "session identity does not match request identity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupContext(t.Context())

			// Generate an auth error
			err := EnforceSessionIdentityPublicKeyMatches(ctx, tt.config, tt.identityPubKey)
			require.Error(t, err)

			var authzErr *Error
			require.ErrorAs(t, err, &authzErr)

			// Imitate how tokens.FormatErrorWith... would wrap the error in real use
			formattedErr := fmt.Errorf("dummy message: %w", err)

			// Test that the status functions that an "interceptor" would use extract the correct values
			code := status.Code(formattedErr)
			assert.Equal(t, tt.expectedCode, code, "status.Code() for auth error is wrong")

			convertedSt := status.Convert(formattedErr)
			assert.Equal(t, tt.expectedCode, convertedSt.Code(), "status.Convert() for auth error is wrong")
			assert.Contains(t, convertedSt.Message(), tt.expectedMsg, "status.Convert() should preserve the error message")

			fromErrSt, ok := status.FromError(formattedErr)
			require.True(t, ok, "status.FromError() should work with auth error")
			assert.Equal(t, tt.expectedCode, fromErrSt.Code(), "status.FromError() for auth error is wrong")
			assert.Contains(t, fromErrSt.Message(), tt.expectedMsg, "status.FromError() should preserve the error message")
		})
	}
}
