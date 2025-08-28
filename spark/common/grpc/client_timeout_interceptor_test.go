package common

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// mockTimeoutProvider is a simple implementation of TimeoutProvider for testing
type mockTimeoutProvider struct {
	timeout time.Duration
}

func (m *mockTimeoutProvider) GetTimeoutForMethod(method string) time.Duration {
	return m.timeout
}

// TestClientTimeoutInterceptor_TimesOut verifies that the interceptor applies a per-request
// timeout and that a blocking invoker returns context.DeadlineExceeded when the timeout elapses.
func TestClientTimeoutInterceptor_TimesOut(t *testing.T) {
	// Use a short timeout to make the test fast and reliable.
	const timeout = 100 * time.Millisecond

	timeoutProvider := &mockTimeoutProvider{timeout: timeout}
	interceptor := ClientTimeoutInterceptor(timeoutProvider)

	// invoker blocks until the provided context is done, then returns ctx.Err().
	invoker := func(ctx context.Context, _ string, _ any, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout * 10):
			// Should never happen if interceptor timeout is enforced.
			return nil
		}
	}

	start := time.Now()
	err := interceptor(t.Context(), "/test.Service/TestMethod", nil, nil, nil, invoker)
	elapsed := time.Since(start)

	// Expect the interceptor to enforce timeout and return DeadlineExceeded.
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// Sanity check on duration: should be at least the timeout, but not excessively larger.
	require.GreaterOrEqual(t, elapsed, timeout)
	require.Less(t, elapsed, 2*time.Second)
}

func TestClientTimeoutInterceptor_NoTimeout(t *testing.T) {
	// Use a 0 timeout to test that the interceptor does not enforce a timeout.
	const timeout = 0 * time.Second

	timeoutProvider := &mockTimeoutProvider{timeout: timeout}
	interceptor := ClientTimeoutInterceptor(timeoutProvider)

	// invoker waits for a reasonable duration to verify no timeout is applied.
	invoker := func(ctx context.Context, _ string, _ any, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			// If we reach here, no timeout was applied - return success
			return nil
		}
	}

	start := time.Now()
	err := interceptor(t.Context(), "/test.Service/TestMethod", nil, nil, nil, invoker)
	elapsed := time.Since(start)

	// Expect the interceptor to not enforce timeout and return nil.
	require.NoError(t, err)

	// Sanity check on duration: should be at least 500ms (our wait time), but not excessively larger.
	require.GreaterOrEqual(t, elapsed, 500*time.Millisecond)
	require.Less(t, elapsed, 1*time.Second)
}
