package common

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestIdempotencyKeyClientInterceptor_SetsHeader(t *testing.T) {
	interceptor := IdempotencyKeyClientInterceptor()

	var capturedCtx context.Context
	fakeInvoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		capturedCtx = ctx
		return nil
	}

	err := interceptor(t.Context(), "/test.Service/Method", nil, nil, nil, fakeInvoker)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	md, ok := metadata.FromOutgoingContext(capturedCtx)
	if !ok {
		t.Fatal("expected outgoing metadata to be set")
	}

	values := md.Get("x-idempotency-key")
	if len(values) != 1 {
		t.Fatalf("expected exactly 1 idempotency key, got %d", len(values))
	}

	if _, err := uuid.Parse(values[0]); err != nil {
		t.Fatalf("idempotency key is not a valid UUID: %s", values[0])
	}
}

func TestIdempotencyKeyClientInterceptor_UniquePerCall(t *testing.T) {
	interceptor := IdempotencyKeyClientInterceptor()

	var keys []string
	fakeInvoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		keys = append(keys, md.Get("x-idempotency-key")[0])
		return nil
	}

	for range 3 {
		err := interceptor(t.Context(), "/test.Service/Method", nil, nil, nil, fakeInvoker)
		if err != nil {
			t.Fatalf("interceptor returned error: %v", err)
		}
	}

	seen := make(map[string]bool)
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate idempotency key generated: %s", k)
		}
		seen[k] = true
	}
}

func TestIdempotencyKeyClientInterceptor_PreservesExistingMetadata(t *testing.T) {
	interceptor := IdempotencyKeyClientInterceptor()

	ctx := metadata.AppendToOutgoingContext(t.Context(), "existing-key", "existing-value")

	var capturedCtx context.Context
	fakeInvoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		capturedCtx = ctx
		return nil
	}

	err := interceptor(ctx, "/test.Service/Method", nil, nil, nil, fakeInvoker)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	md, _ := metadata.FromOutgoingContext(capturedCtx)

	existingValues := md.Get("existing-key")
	if len(existingValues) != 1 || existingValues[0] != "existing-value" {
		t.Fatalf("expected existing metadata to be preserved, got %v", existingValues)
	}

	idempotencyValues := md.Get("x-idempotency-key")
	if len(idempotencyValues) != 1 {
		t.Fatalf("expected idempotency key to be set alongside existing metadata")
	}
}
