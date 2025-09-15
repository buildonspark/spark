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
		targetLimit  *float64
		target       string
		acquisitions int
		methodLimit  map[string]int64
		globalLimit  int64
	}{
		{
			name:         "method limit - within bounds",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": 3},
			acquisitions: 2,
			globalLimit:  10,
		},
		{
			name:         "method limit - at bounds",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": 3},
			acquisitions: 3,
			globalLimit:  10,
		},
		{
			name:         "method limit - zero limit - unlimited",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": 0},
			acquisitions: 2,
			globalLimit:  10,
		},
		{
			name:         "method limit - negative limit - unlimited",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": -1},
			acquisitions: 1,
			globalLimit:  10,
		},
		{
			name:         "global limit - within limit",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": -1},
			acquisitions: 1,
			globalLimit:  2,
		},
		{
			name:         "global limit - at limit",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": -1},
			acquisitions: 2,
			globalLimit:  2,
		},
		{
			name:         "global limit - unlimited",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": -1},
			acquisitions: 2,
			globalLimit:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			knobValues := map[string]float64{}
			if tt.methodLimit != nil {
				for method, limit := range tt.methodLimit {
					knobValues[fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, method)] = float64(limit)
				}
			}
			mockKnobs := knobs.NewFixedKnobs(knobValues)
			guard := NewConcurrencyGuard(mockKnobs, tt.globalLimit)

			// Acquire multiple times
			for i := 0; i < tt.acquisitions; i++ {
				err := guard.TryAcquireMethod("/test.Service/TestMethod")
				require.NoError(t, err)
			}

			// Verify internal state
			concurrencyGuard := guard.(*ConcurrencyGuard)
			require.Equal(t, int64(tt.acquisitions), concurrencyGuard.counterMap["/test.Service/TestMethod"])
		})
	}
}

func TestConcurrencyGuard_AcquireTarget_ExceedsLimit(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		acquisitions int
		methodLimit  map[string]int64
		globalLimit  int64
	}{
		{
			name:         "method limit exceeded - beyond bounds",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": 3},
			acquisitions: 4,
			globalLimit:  10,
		},
		{
			name:         "global limit exceeded",
			methodLimit:  map[string]int64{"/test.Service/TestMethod": -1},
			acquisitions: 2,
			globalLimit:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			knobValues := map[string]float64{}
			if tt.methodLimit != nil {
				for method, limit := range tt.methodLimit {
					knobValues[fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, method)] = float64(limit)
				}
			}
			mockKnobs := knobs.NewFixedKnobs(knobValues)
			guard := NewConcurrencyGuard(mockKnobs, tt.globalLimit)

			var err error
			for i := 0; i < tt.acquisitions; i++ {
				err = guard.TryAcquireMethod("/test.Service/TestMethod")
			}
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
		guard := NewConcurrencyGuard(mockKnobs, 10)

		// Acquire some resources
		for i := 0; i < 3; i++ {
			err := guard.TryAcquireMethod("TestMethod")
			require.NoError(t, err)
		}

		// Verify current count
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(3), concurrencyGuard.counterMap["TestMethod"])

		// Release resources
		for i := 0; i < 3; i++ {
			guard.ReleaseMethod("TestMethod")
		}

		// Verify count is back to zero
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])

		// Release again to verify it doesn't go negative
		guard.ReleaseMethod("TestMethod")
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])
	})

	t.Run("release can not go negative", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 10)

		// Release without acquiring - this will make counter negative
		guard.ReleaseMethod("TestMethod")

		// Verify counter is still 0
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])

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
				err := guard.TryAcquireMethod("TestMethod")
				if err != nil {
					errors[idx] = err
					return
				}

				// Small sleep to increase chance of race conditions
				time.Sleep(time.Microsecond)

				// Release
				guard.ReleaseMethod("TestMethod")
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
	assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"], "Final count should be zero after all releases")
}

func TestConcurrencyInterceptor(t *testing.T) {
	t.Run("successful request within limit", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 1)
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
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])
	})

	t.Run("request exceeding limit", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 1)
		interceptor := ConcurrencyInterceptor(guard)

		// First acquire the only slot
		err := guard.TryAcquireMethod("/test.Service/TestMethod")
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
		guard := NewConcurrencyGuard(mockKnobs, 10)
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
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])
	})

	t.Run("handler error still releases resource", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		guard := NewConcurrencyGuard(mockKnobs, 10)
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
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])
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

func TestConcurrencyGuard_AcquireAfterGlobalLimit(t *testing.T) {
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
	guard := NewConcurrencyGuard(mockKnobs, 3)

	// Acquire some resources
	for i := 0; i < 3; i++ {
		err := guard.TryAcquireMethod("TestMethod")
		require.NoError(t, err)
	}

	// Verify current count
	concurrencyGuard := guard.(*ConcurrencyGuard)
	assert.Equal(t, int64(3), concurrencyGuard.counterMap["TestMethod"])

	// Acquiring again fails
	err := guard.TryAcquireMethod("TestMethod")
	require.Error(t, err)

	// Method counter is still at 3
	assert.Equal(t, int64(3), concurrencyGuard.counterMap["TestMethod"])

	guard.ReleaseMethod("TestMethod")

	// Global counter is decremented
	assert.Equal(t, int64(2), concurrencyGuard.globalCounter)

	// Acquiring after release works
	err = guard.TryAcquireMethod("TestMethod")
	require.NoError(t, err)
}
