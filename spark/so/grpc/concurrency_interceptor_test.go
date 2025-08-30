package grpc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConcurrencyGuard_Acquire_WithinLimit(t *testing.T) {
	tests := []struct {
		name         string
		defaultLimit int
		targetLimit  *float64
		target       string
		acquisitions int
	}{
		{
			name:         "default limit - within bounds",
			defaultLimit: 5,
			acquisitions: 3,
		},
		{
			name:         "default limit - at bounds",
			defaultLimit: 3,
			acquisitions: 3,
		},
		{
			name:         "zero limit - unlimited",
			defaultLimit: 5,
			targetLimit:  floatPtr(0),
			acquisitions: 1,
		},
		{
			name:         "negative limit - default limit",
			defaultLimit: 5,
			targetLimit:  floatPtr(-1),
			acquisitions: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			knobValues := map[string]float64{}
			if tt.targetLimit != nil {
				knobValues[knobs.KnobGrpcServerConcurrencyLimitLimit] = *tt.targetLimit
			}
			mockKnobs := knobs.NewFixedKnobs(knobValues)

			guard := NewConcurrencyGuard(mockKnobs, tt.defaultLimit)

			// Acquire multiple times
			for i := 0; i < tt.acquisitions; i++ {
				err := guard.TryAcquire()
				require.NoError(t, err)
			}

			// Verify internal state
			concurrencyGuard := guard.(*ConcurrencyGuard)
			require.Equal(t, int64(tt.acquisitions), concurrencyGuard.current)
		})
	}
}

func TestConcurrencyGuard_AcquireTarget_ExceedsLimit(t *testing.T) {
	tests := []struct {
		name         string
		defaultLimit int
		targetLimit  *float64
		target       string
		acquisitions int
	}{
		{
			name:         "default limit exceeded",
			defaultLimit: 3,
			acquisitions: 4,
		},
		{
			name:         "negative limit - default limit",
			defaultLimit: 2,
			targetLimit:  floatPtr(-1),
			acquisitions: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			knobValues := map[string]float64{}
			if tt.targetLimit != nil {
				knobValues[knobs.KnobGrpcServerConcurrencyLimitLimit] = *tt.targetLimit
			}
			mockKnobs := knobs.NewFixedKnobs(knobValues)

			guard := NewConcurrencyGuard(mockKnobs, tt.defaultLimit)

			// Acquire up to limit
			limit := tt.defaultLimit
			if tt.targetLimit != nil && *tt.targetLimit > 0 {
				limit = int(*tt.targetLimit)
			}

			// Acquire within limit first
			for i := 0; i < limit; i++ {
				err := guard.TryAcquire()
				require.NoError(t, err)
			}

			// This should fail
			err := guard.TryAcquire()
			require.Error(t, err)

			st, ok := status.FromError(err)
			require.True(t, ok)
			require.Equal(t, codes.ResourceExhausted, st.Code())
		})
	}
}

func TestConcurrencyGuard_Release(t *testing.T) {
	t.Run("normal release", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 5)

		// Acquire some resources
		for i := 0; i < 3; i++ {
			err := guard.TryAcquire()
			require.NoError(t, err)
		}

		// Verify current count
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(3), concurrencyGuard.current)

		// Release resources
		for i := 0; i < 3; i++ {
			guard.Release()
		}

		// Verify count is back to zero
		assert.Equal(t, int64(0), concurrencyGuard.current)

		// Release again to verify it doesn't go negative
		guard.Release()
		assert.Equal(t, int64(0), concurrencyGuard.current)
	})

	t.Run("release can not go negative", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 5)

		// Release without acquiring - this will make counter negative
		guard.Release()

		// Verify counter is still 0
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.current)

	})
}

func TestConcurrencyGuard_ConcurrentAccess(t *testing.T) {
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
	guard := NewConcurrencyGuard(mockKnobs, 100) // High limit for concurrent test

	numGoroutines := 50
	numOperationsPerGoroutine := 20

	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	// Launch multiple goroutines that acquire and release concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			for j := 0; j < numOperationsPerGoroutine; j++ {
				// Acquire
				err := guard.TryAcquire()
				if err != nil {
					errors[idx] = err
					return
				}

				// Small sleep to increase chance of race conditions
				time.Sleep(time.Microsecond)

				// Release
				guard.Release()
			}
		}(i)
	}

	wg.Wait()

	// Check for any errors
	for i, err := range errors {
		if err != nil {
			t.Fatalf("Goroutine %d encountered error: %v", i, err)
		}
	}

	// Verify final state
	concurrencyGuard := guard.(*ConcurrencyGuard)
	assert.Equal(t, int64(0), concurrencyGuard.current, "Final count should be zero after all releases")
}

func TestConcurrencyInterceptor(t *testing.T) {
	t.Run("successful request within limit", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 5)
		interceptor := ConcurrencyInterceptor(guard)

		called := false
		handler := func(ctx context.Context, req any) (any, error) {
			called = true
			return "success", nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/test.Service/TestMethod",
		}

		resp, err := interceptor(t.Context(), nil, info, handler)

		require.NoError(t, err)
		assert.Equal(t, "success", resp)
		assert.True(t, called)

		// Verify resource was released
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.current)
	})

	t.Run("request exceeding limit", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 1)
		interceptor := ConcurrencyInterceptor(guard)

		// First acquire the only slot
		err := guard.TryAcquire()
		require.NoError(t, err)

		called := false
		handler := func(ctx context.Context, req any) (any, error) {
			called = true
			return "success", nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/test.Service/TestMethod",
		}

		resp, err := interceptor(t.Context(), nil, info, handler)

		require.Error(t, err)
		assert.Nil(t, resp)
		assert.False(t, called)

		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.ResourceExhausted, st.Code())
		assert.Contains(t, err.Error(), "concurrency limit exceeded")
	})

	t.Run("handler panic still releases resource", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 5)
		interceptor := ConcurrencyInterceptor(guard)

		handler := func(ctx context.Context, req any) (any, error) {
			panic("test panic")
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/test.Service/TestMethod",
		}

		// Should panic but still release the resource
		assert.Panics(t, func() {
			_, err := interceptor(t.Context(), nil, info, handler)
			require.NoError(t, err)
		})

		// Verify resource was released despite panic
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.current)
	})

	t.Run("handler error still releases resource", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 5)
		interceptor := ConcurrencyInterceptor(guard)

		expectedErr := fmt.Errorf("handler error")
		handler := func(ctx context.Context, req any) (any, error) {
			return nil, expectedErr
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/test.Service/TestMethod",
		}

		resp, err := interceptor(t.Context(), nil, info, handler)

		require.Error(t, err)
		assert.Equal(t, expectedErr, err)
		assert.Nil(t, resp)

		// Verify resource was released
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.current)
	})

	t.Run("with noop limiter", func(t *testing.T) {
		limiter := &NoopResourceLimiter{}
		interceptor := ConcurrencyInterceptor(limiter)

		called := false
		handler := func(ctx context.Context, req any) (any, error) {
			called = true
			return "success", nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/test.Service/TestMethod",
		}

		resp, err := interceptor(t.Context(), nil, info, handler)

		require.NoError(t, err)
		assert.Equal(t, "success", resp)
		assert.True(t, called)
	})
}

// Helper function to create float64 pointer
func floatPtr(f float64) *float64 {
	return &f
}
