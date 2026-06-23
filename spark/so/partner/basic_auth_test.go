package partner

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	partnerMethod = "/spark_partner.SparkPartnerService/query_spark_transaction_volumes"
	otherMethod   = "/spark.SparkService/query_balance"
)

// newBasicAuthInterceptorWithLookup builds an interceptor with a stubbed secret-hash lookup, for testing.
func newBasicAuthInterceptorWithLookup(lookup func(ctx context.Context, partnerID string) (string, error)) *BasicAuthInterceptor {
	return &BasicAuthInterceptor{
		lookupSecretHash: lookup,
		argon2Sem:        make(chan struct{}, maxConcurrentArgon2Verifications),
	}
}

// makeArgon2idHash produces a PHC-encoded argon2id hash, matching what an operator
// stores in partner_keys.basic_auth_secret_hash.
func makeArgon2idHash(secret string) string {
	salt := []byte("0123456789abcdef")
	h := argon2.IDKey([]byte(secret), salt, argon2idTime, argon2idMemKiB, argon2idThreads, argon2idKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2idMemKiB, argon2idTime, argon2idThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(h),
	)
}

func basicAuthHeader(partnerID, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(partnerID+":"+secret))
}

func partnerCtx(t *testing.T, authValue string) context.Context {
	if authValue == "" {
		return t.Context()
	}
	return metadata.NewIncomingContext(t.Context(), metadata.Pairs(authorizationHeader, authValue))
}

// recordingHandler is a grpc.UnaryHandler that records whether it ran and the PartnerInfo
// the interceptor injected into its context.
type recordingHandler struct {
	called bool
	seen   *PartnerInfo
}

func (r *recordingHandler) handle(ctx context.Context, _ any) (any, error) {
	r.called = true
	if pInfo, ok := GetPartnerInfoFromContext(ctx); ok {
		r.seen = pInfo
	}
	return "ok", nil
}

// Methods outside SparkPartnerService pass through untouched, without consulting the credential lookup.
func TestBasicAuthInterceptor_PassesThroughOtherServices(t *testing.T) {
	i := newBasicAuthInterceptorWithLookup(func(context.Context, string) (string, error) {
		t.Fatal("lookup should not be called for non-partner methods")
		return "", nil
	})
	var h recordingHandler
	resp, err := i.UnaryServerInterceptor(partnerCtx(t, ""), nil, &grpc.UnaryServerInfo{FullMethod: otherMethod}, h.handle)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, h.called)
}

// Valid base64(partner_id:secret) whose secret matches the stored argon2id hash authenticates,
// invokes the handler, and injects the partner identity into the handler's context.
func TestBasicAuthInterceptor_ValidCredentials(t *testing.T) {
	i := newBasicAuthInterceptorWithLookup(func(_ context.Context, partnerID string) (string, error) {
		require.Equal(t, "breez", partnerID)
		return makeArgon2idHash("s3cret"), nil
	})
	var h recordingHandler
	ctx := partnerCtx(t, basicAuthHeader("breez", "s3cret"))
	_, err := i.UnaryServerInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: partnerMethod}, h.handle)
	require.NoError(t, err)
	require.True(t, h.called)
	require.NotNil(t, h.seen)
	require.Equal(t, "breez", h.seen.PartnerID)
}

// A secret containing colons is preserved (only the first colon splits id:secret).
func TestBasicAuthInterceptor_SecretWithColons(t *testing.T) {
	i := newBasicAuthInterceptorWithLookup(func(context.Context, string) (string, error) {
		return makeArgon2idHash("a:b:c"), nil
	})
	var h recordingHandler
	ctx := partnerCtx(t, basicAuthHeader("breez", "a:b:c"))
	_, err := i.UnaryServerInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: partnerMethod}, h.handle)
	require.NoError(t, err)
	require.True(t, h.called)
}

// All failure modes return Unauthenticated and never invoke the handler. This exercises header
// parsing, the DB lookup, and argon2id verification (wrong secret and malformed stored hash) through
// the public interceptor.
func TestBasicAuthInterceptor_Rejects(t *testing.T) {
	goodHash := func(context.Context, string) (string, error) { return makeArgon2idHash("s3cret"), nil }

	cases := []struct {
		name   string
		auth   string
		lookup func(context.Context, string) (string, error)
	}{
		{"missing authorization header", "", goodHash},
		{"wrong scheme", "Bearer " + base64.StdEncoding.EncodeToString([]byte("breez:s3cret")), goodHash},
		{"malformed base64", "Basic not-base64!!", goodHash},
		{"no colon in credentials", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")), goodHash},
		{"empty secret", "Basic " + base64.StdEncoding.EncodeToString([]byte("breez:")), goodHash},
		{"wrong secret", basicAuthHeader("breez", "wrong"), goodHash},
		{"unknown partner", basicAuthHeader("nobody", "s3cret"), func(context.Context, string) (string, error) { return "", nil }},
		{"partner has no secret configured", basicAuthHeader("breez", "s3cret"), func(context.Context, string) (string, error) { return "", nil }},
		{"malformed stored hash", basicAuthHeader("breez", "s3cret"), func(context.Context, string) (string, error) { return "not-a-valid-phc-hash", nil }},
		// A stored PHC with an empty hash component must NOT authenticate any secret.
		{"empty hash component in stored PHC", basicAuthHeader("breez", "anything"), func(context.Context, string) (string, error) {
			return "$argon2id$v=19$m=65536,t=3,p=4$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef")) + "$", nil
		}},
		{"empty salt component in stored PHC", basicAuthHeader("breez", "anything"), func(context.Context, string) (string, error) {
			return "$argon2id$v=19$m=65536,t=3,p=4$$" + base64.RawStdEncoding.EncodeToString([]byte("deadbeefdeadbeefdeadbeefdeadbeef")), nil
		}},
		// Zero argon2 params would make argon2.IDKey panic; they must be rejected instead.
		{"zero argon2 time param", basicAuthHeader("breez", "anything"), func(context.Context, string) (string, error) {
			return "$argon2id$v=19$m=65536,t=0,p=4$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef")) + "$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")), nil
		}},
		{"zero argon2 parallelism param", basicAuthHeader("breez", "anything"), func(context.Context, string) (string, error) {
			return "$argon2id$v=19$m=65536,t=3,p=0$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef")) + "$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")), nil
		}},
		// An absurd memory cost must be rejected before reaching argon2.IDKey (which would try to
		// allocate it). m=4294967295 KiB (~4 TiB) is well past argon2idMaxMemKiB.
		{"argon2 memory param exceeds bound", basicAuthHeader("breez", "anything"), func(context.Context, string) (string, error) {
			return "$argon2id$v=19$m=4294967295,t=3,p=4$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef")) + "$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")), nil
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := newBasicAuthInterceptorWithLookup(tc.lookup)
			handlerCalled := false
			h := func(ctx context.Context, _ any) (any, error) {
				handlerCalled = true
				return nil, nil
			}
			ctx := partnerCtx(t, tc.auth)
			_, err := i.UnaryServerInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: partnerMethod}, h)
			require.Error(t, err)
			require.Equal(t, codes.Unauthenticated, status.Code(err))
			require.False(t, handlerCalled, "handler must not run on auth failure")
		})
	}
}

// A lookup failure (e.g. DB down) is an operator-side outage, not a credential problem, so it must
// surface as Unavailable rather than Unauthenticated.
func TestBasicAuthInterceptor_DBOutageIsUnavailable(t *testing.T) {
	i := newBasicAuthInterceptorWithLookup(func(context.Context, string) (string, error) {
		return "", fmt.Errorf("connection refused")
	})
	handlerCalled := false
	h := func(context.Context, any) (any, error) { handlerCalled = true; return nil, nil }
	ctx := partnerCtx(t, basicAuthHeader("breez", "s3cret"))
	_, err := i.UnaryServerInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: partnerMethod}, h)
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.False(t, handlerCalled)
}

// When all argon2id slots are occupied, further verifications are shed with ResourceExhausted
// rather than piling up unbounded (expensive) argon2 work.
func TestBasicAuthInterceptor_Argon2OverloadIsResourceExhausted(t *testing.T) {
	i := newBasicAuthInterceptorWithLookup(func(context.Context, string) (string, error) {
		return makeArgon2idHash("s3cret"), nil
	})
	// Saturate the semaphore so the next verification can't acquire a slot.
	for n := 0; n < cap(i.argon2Sem); n++ {
		i.argon2Sem <- struct{}{}
	}
	handlerCalled := false
	h := func(context.Context, any) (any, error) { handlerCalled = true; return nil, nil }
	ctx := partnerCtx(t, basicAuthHeader("breez", "s3cret"))
	_, err := i.UnaryServerInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: partnerMethod}, h)
	require.Error(t, err)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.False(t, handlerCalled)
}
