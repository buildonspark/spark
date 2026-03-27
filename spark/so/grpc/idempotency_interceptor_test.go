package grpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/idempotencykey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// we need to use Postgres here for row-level locking, SQLite does not support it.
func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()
	m.Run()
}

func TestIdempotencyInterceptor_CacheHit(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	idempotencyKey := "cache-hit-key-123"
	methodName := "my_method"
	apiResp := map[string]any{"foo": "bar"}

	// First call: Creates the record and stores the response
	interceptorCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		interceptorCalled = true
		structResp, err := structpb.NewStruct(apiResp)
		return structResp, err
	}

	resp1, err := callInterceptor(t, ctx, idempotencyKey, methodName, handler)
	require.NoError(t, err)
	assert.True(t, interceptorCalled, "handler should be called on first request")

	// Second call: Should be a cache hit and not call the handler
	interceptorCalled = false
	handler2 := func(ctx context.Context, req any) (any, error) {
		interceptorCalled = true
		return nil, fmt.Errorf("should not be called")
	}

	resp2, err := callInterceptor(t, ctx, idempotencyKey, methodName, handler2)
	require.NoError(t, err)
	assert.False(t, interceptorCalled, "handler should not be called on cache hit")

	// Verify both responses are identical
	structResp1, ok := resp1.(*structpb.Struct)
	require.True(t, ok)
	structResp2, ok := resp2.(*structpb.Struct)
	require.True(t, ok)
	assert.EqualExportedValues(t, structResp1, structResp2)
	assert.Equal(t, "bar", structResp2.Fields["foo"].GetStringValue())
}

func TestIdempotencyInterceptor_CacheMissSuccessfulStore(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	apiResp, err := structpb.NewStruct(map[string]any{"foo": "bar"})
	require.NoError(t, err)

	idempotencyKey := "cache-miss-key-456"
	methodName := "my_method"

	handler := func(ctx context.Context, req any) (any, error) {
		return apiResp, nil
	}

	resp, err := callInterceptor(t, ctx, idempotencyKey, methodName, handler)
	require.NoError(t, err)

	// Does the response look good?
	structResp, ok := resp.(*structpb.Struct)
	require.True(t, ok)
	assert.Equal(t, "bar", structResp.Fields["foo"].GetStringValue())

	// Does the DB record look good?
	storedKey, err := tx.IdempotencyKey.Query().
		Where(
			idempotencykey.IdempotencyKeyEQ(idempotencyKey),
			idempotencykey.MethodNameEQ(methodName),
		).
		Only(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, storedKey.Response)
	assert.Equal(t, methodName, storedKey.MethodName)

	var storedAny anypb.Any
	err = protojson.Unmarshal(storedKey.Response, &storedAny)
	require.NoError(t, err)

	unwrappedMsg, err := storedAny.UnmarshalNew()
	require.NoError(t, err)

	assert.EqualExportedValues(t, apiResp, unwrappedMsg)
}

func TestIdempotencyInterceptor_HandlerError(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	expectedError := fmt.Errorf("handler error")
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, expectedError
	}

	_, err = callInterceptor(t, ctx, "test-handler-error", "my_method", handler)

	assert.Equal(t, expectedError, err)

	// In production we always open a transaction, which would rollback and delete the record.
	// In this test context without the middleware, the record persists, but so do I
	count, err := tx.IdempotencyKey.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestIdempotencyInterceptor_RetryAfterError(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	idempotencyKey := "test-retry-after-error"
	// Fail the first request
	failingHandler := func(ctx context.Context, req any) (any, error) {
		return nil, fmt.Errorf("temporary error")
	}

	_, err := callInterceptor(t, ctx, idempotencyKey, "my_method", failingHandler)
	require.Error(t, err)

	// Retry with same idempotency key should succeed
	successHandler := func(ctx context.Context, req any) (any, error) {
		structResp, err := structpb.NewStruct(map[string]any{"foo": "bar"})
		return structResp, err
	}
	resp, err := callInterceptor(t, ctx, idempotencyKey, "my_method", successHandler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestIdempotencyInterceptor_SameKeyDifferentMethods(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	idempotencyKey := "shared-key-123"

	// First request to method1
	handler1Called := false
	handler1 := func(ctx context.Context, req any) (any, error) {
		handler1Called = true
		structResp, err := structpb.NewStruct(map[string]any{"foo": "bar"})
		return structResp, err
	}

	resp1, err := callInterceptor(t, ctx, idempotencyKey, "method1", handler1)
	require.NoError(t, err)
	assert.True(t, handler1Called)

	// Second request to method2
	handler2Called := false
	handler2 := func(ctx context.Context, req any) (any, error) {
		handler2Called = true
		structResp, err := structpb.NewStruct(map[string]any{"bar": "foo"})
		return structResp, err
	}

	resp2, err := callInterceptor(t, ctx, idempotencyKey, "method2", handler2)
	require.NoError(t, err)
	assert.True(t, handler2Called)
	assert.NotNil(t, resp2)

	// making sure we got different responses
	structResp1, ok1 := resp1.(*structpb.Struct)
	require.True(t, ok1)
	structResp2, ok2 := resp2.(*structpb.Struct)
	require.True(t, ok2)
	assert.False(t, proto.Equal(structResp1, structResp2))

	// Verify both records exist in the database
	records, err := tx.IdempotencyKey.Query().
		Where(idempotencykey.IdempotencyKeyEQ(idempotencyKey)).
		All(ctx)
	require.NoError(t, err)
	assert.Len(t, records, 2)

	// Verify the records have different method names, but the same idempotency key
	methodNames := make(map[string]bool)
	for _, record := range records {
		assert.Equal(t, idempotencyKey, record.IdempotencyKey)
		methodNames[record.MethodName] = true
	}
	assert.Contains(t, methodNames, "method1")
	assert.Contains(t, methodNames, "method2")

	// Third request to method1 should be a cache hit
	handler3Called := false
	handler3 := func(ctx context.Context, req any) (any, error) {
		handler3Called = true
		return nil, fmt.Errorf("should not be called")
	}

	resp3, err := callInterceptor(t, ctx, idempotencyKey, "method1", handler3)
	require.NoError(t, err)
	assert.False(t, handler3Called)

	structResp3, ok3 := resp3.(*structpb.Struct)
	require.True(t, ok3)

	assert.EqualExportedValues(t, structResp1, structResp3)
}

func TestIdempotencyInterceptor_SkipsReadOnlySession(t *testing.T) {
	// Create a context with a read-only session
	dbClient := db.NewTestSQLiteClient(t)
	readOnlySession := db.NewReadOnlySession(t.Context(), dbClient)
	ctx := ent.Inject(t.Context(), readOnlySession)

	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return &structpb.Struct{}, nil
	}

	// Even with an idempotency key, the interceptor should skip idempotency
	// processing and call the handler directly for read-only sessions.
	resp, err := callInterceptor(t, ctx, "some-idempotency-key", "some_method", handler)
	require.NoError(t, err)
	assert.True(t, handlerCalled, "handler should be called directly for read-only sessions")
	assert.NotNil(t, resp)
}

func TestExtractIdempotencyKey(t *testing.T) {
	tests := []struct {
		name        string
		setupCtx    func() context.Context
		expectedKey string
	}{
		{
			name: "with idempotency key",
			setupCtx: func() context.Context {
				md := metadata.Pairs(common.IdempotencyKeyHeader, "test-key-123")
				return metadata.NewIncomingContext(t.Context(), md)
			},
			expectedKey: "test-key-123",
		},
		{
			name: "without metadata",
			setupCtx: func() context.Context {
				return t.Context()
			},
			expectedKey: "",
		},
		{
			name: "with empty key",
			setupCtx: func() context.Context {
				md := metadata.Pairs(common.IdempotencyKeyHeader, "")
				return metadata.NewIncomingContext(t.Context(), md)
			},
			expectedKey: "",
		},
		{
			name: "with different header",
			setupCtx: func() context.Context {
				md := metadata.Pairs("some-other-header", "value")
				return metadata.NewIncomingContext(t.Context(), md)
			},
			expectedKey: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()
			key := extractIdempotencyKey(ctx)
			assert.Equal(t, tt.expectedKey, key)
		})
	}
}

func TestIdempotencyInterceptor_DifferentIdentitiesSeparateCaches(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	idempotencyKey := "cross-user-key"
	methodName := "my_method"
	identityA := "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	identityB := "02c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"

	// User A caches a response
	handlerA := func(ctx context.Context, req any) (any, error) {
		return structpb.NewStruct(map[string]any{"user": "A"})
	}
	respA, err := callInterceptorWithIdentity(t, ctx, idempotencyKey, methodName, identityA, handlerA)
	require.NoError(t, err)

	// User B with same key should NOT get User A's response
	handlerBCalled := false
	handlerB := func(ctx context.Context, req any) (any, error) {
		handlerBCalled = true
		return structpb.NewStruct(map[string]any{"user": "B"})
	}
	respB, err := callInterceptorWithIdentity(t, ctx, idempotencyKey, methodName, identityB, handlerB)
	require.NoError(t, err)
	assert.True(t, handlerBCalled, "handler should be called for different identity")

	structA, ok := respA.(*structpb.Struct)
	require.True(t, ok)
	structB, ok := respB.(*structpb.Struct)
	require.True(t, ok)
	assert.Equal(t, "A", structA.Fields["user"].GetStringValue())
	assert.Equal(t, "B", structB.Fields["user"].GetStringValue())
}

func TestIdempotencyInterceptor_SameIdentityCacheHit(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	idempotencyKey := "same-user-key"
	methodName := "my_method"
	identity := "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

	handler := func(ctx context.Context, req any) (any, error) {
		return structpb.NewStruct(map[string]any{"user": "A"})
	}
	_, err := callInterceptorWithIdentity(t, ctx, idempotencyKey, methodName, identity, handler)
	require.NoError(t, err)

	// Same identity, same key — should be a cache hit
	handlerCalled := false
	handler2 := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return nil, fmt.Errorf("should not be called")
	}
	resp2, err := callInterceptorWithIdentity(t, ctx, idempotencyKey, methodName, identity, handler2)
	require.NoError(t, err)
	assert.False(t, handlerCalled, "handler should not be called on cache hit for same identity")

	structResp, ok := resp2.(*structpb.Struct)
	require.True(t, ok)
	assert.Equal(t, "A", structResp.Fields["user"].GetStringValue())
}

func TestIdempotencyInterceptor_NoIdentitySharesCache(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	idempotencyKey := "internal-key"
	methodName := "my_method"

	handler := func(ctx context.Context, req any) (any, error) {
		return structpb.NewStruct(map[string]any{"internal": "response"})
	}
	_, err := callInterceptor(t, ctx, idempotencyKey, methodName, handler)
	require.NoError(t, err)

	// Second internal call (no identity) — should be cache hit
	handlerCalled := false
	handler2 := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return nil, fmt.Errorf("should not be called")
	}
	resp2, err := callInterceptor(t, ctx, idempotencyKey, methodName, handler2)
	require.NoError(t, err)
	assert.False(t, handlerCalled, "internal calls with same key should share cache")
	assert.NotNil(t, resp2)
}

func TestIdempotencyInterceptor_IdentityDoesNotMatchNoIdentity(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	idempotencyKey := "mixed-key"
	methodName := "my_method"
	identity := "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

	// Internal call (no identity) caches a response
	handler := func(ctx context.Context, req any) (any, error) {
		return structpb.NewStruct(map[string]any{"from": "internal"})
	}
	_, err := callInterceptor(t, ctx, idempotencyKey, methodName, handler)
	require.NoError(t, err)

	// Authenticated call with same key should NOT get the internal response
	handlerCalled := false
	handler2 := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return structpb.NewStruct(map[string]any{"from": "user"})
	}
	resp2, err := callInterceptorWithIdentity(t, ctx, idempotencyKey, methodName, identity, handler2)
	require.NoError(t, err)
	assert.True(t, handlerCalled, "authenticated call should not match unauthenticated cache entry")

	structResp, ok := resp2.(*structpb.Struct)
	require.True(t, ok)
	assert.Equal(t, "user", structResp.Fields["from"].GetStringValue())
}

func callInterceptor(_ *testing.T, ctx context.Context, key string, methodName string, handler grpc.UnaryHandler) (any, error) {
	md := metadata.Pairs(common.IdempotencyKeyHeader, key)
	ctx = metadata.NewIncomingContext(ctx, md)

	info := &grpc.UnaryServerInfo{FullMethod: methodName}
	interceptor := IdempotencyInterceptor()

	return interceptor(ctx, nil, info, handler)
}

func callInterceptorWithIdentity(_ *testing.T, ctx context.Context, key string, methodName string, identityHex string, handler grpc.UnaryHandler) (any, error) {
	md := metadata.Pairs(common.IdempotencyKeyHeader, key)
	ctx = metadata.NewIncomingContext(ctx, md)
	if identityHex != "" {
		ctx = authn.InjectSessionForTests(ctx, identityHex, 9999999999)
	}

	info := &grpc.UnaryServerInfo{FullMethod: methodName}
	interceptor := IdempotencyInterceptor()

	return interceptor(ctx, nil, info, handler)
}
