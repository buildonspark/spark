package common

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// The service config emitted by BasicClientOptions is invisible through
// higher-level behavior: a *grpc.ClientConn doesn't publicly expose the
// resolved service config, and retry behavior only manifests when a peer
// returns a retryable status. The previous regression — operator.go
// silently overwriting the retry policy by setting a second
// grpc.WithDefaultServiceConfig for load balancing — compiled, dialed, and
// passed every higher-level test, but caused internal SO-to-SO calls to
// give up on the first 502 in production. This narrow contract test locks
// in that the load balancing policy and retry policy coexist in a single
// service config JSON.
func TestDefaultRetryPolicy_IncludesLoadBalancingAndRetries(t *testing.T) {
	var parsed struct {
		LoadBalancingPolicy string `json:"loadBalancingPolicy"`
		MethodConfig        []struct {
			RetryPolicy struct {
				MaxAttempts          int      `json:"MaxAttempts"`
				RetryableStatusCodes []string `json:"RetryableStatusCodes"`
			} `json:"retryPolicy"`
		} `json:"methodConfig"`
	}
	if err := json.Unmarshal([]byte(createRetryPolicy(&defaultRetryPolicy)), &parsed); err != nil {
		t.Fatalf("default service config is not valid JSON: %v", err)
	}
	if parsed.LoadBalancingPolicy != "round_robin" {
		t.Errorf("loadBalancingPolicy: got %q, want round_robin", parsed.LoadBalancingPolicy)
	}
	if len(parsed.MethodConfig) != 1 || parsed.MethodConfig[0].RetryPolicy.MaxAttempts < 2 {
		t.Errorf("expected a methodConfig retryPolicy with MaxAttempts >= 2, got %+v", parsed.MethodConfig)
	}
	retryable := parsed.MethodConfig[0].RetryPolicy.RetryableStatusCodes
	if len(retryable) == 0 || retryable[0] != "UNAVAILABLE" {
		t.Errorf("expected UNAVAILABLE in RetryableStatusCodes, got %v", retryable)
	}
}

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
