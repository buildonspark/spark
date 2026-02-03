package grpc

import (
	"context"
	"fmt"
	"testing"

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

func TestExtractIdempotencyKey(t *testing.T) {
	tests := []struct {
		name        string
		setupCtx    func() context.Context
		expectedKey string
	}{
		{
			name: "with idempotency key",
			setupCtx: func() context.Context {
				md := metadata.Pairs(IdempotencyKeyHeader, "test-key-123")
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
				md := metadata.Pairs(IdempotencyKeyHeader, "")
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

func callInterceptor(_ *testing.T, ctx context.Context, key string, methodName string, handler grpc.UnaryHandler) (any, error) {
	md := metadata.Pairs(IdempotencyKeyHeader, key)
	ctx = metadata.NewIncomingContext(ctx, md)

	info := &grpc.UnaryServerInfo{FullMethod: methodName}
	interceptor := IdempotencyInterceptor()

	return interceptor(ctx, nil, info, handler)
}
