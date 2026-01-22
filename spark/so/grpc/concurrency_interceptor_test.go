package grpc

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
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
			knobValues[fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global")] = float64(tt.globalLimit)
			mockKnobs := knobs.NewFixedKnobs(knobValues)
			guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)

			// Acquire multiple times
			for range tt.acquisitions {
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
			// Set global limit via magic target "global"
			knobValues[fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global")] = float64(tt.globalLimit)
			mockKnobs := knobs.NewFixedKnobs(knobValues)
			guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)

			var err error
			for range tt.acquisitions {
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
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 10,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)

		// Acquire some resources
		for range 3 {
			err := guard.TryAcquireMethod("TestMethod")
			require.NoError(t, err)
		}

		// Verify current count
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(3), concurrencyGuard.counterMap["TestMethod"])

		// Release resources
		for range 3 {
			guard.ReleaseMethod("TestMethod")
		}

		// Verify count is back to zero
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])

		// Release again to verify it doesn't go negative
		guard.ReleaseMethod("TestMethod")
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])
	})

	t.Run("release can not go negative", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 10,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)

		// Release without acquiring - this will make counter negative
		guard.ReleaseMethod("TestMethod")

		// Verify counter is still 0
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["TestMethod"])

	})
}

func TestConcurrencyGuard_ConcurrentAccess(t *testing.T) {
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 100, // High limit for concurrent test
	})
	guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)

	numGoroutines := 50
	numOperationsPerGoroutine := 20

	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	// Launch multiple goroutines that acquire and release concurrently
	for idx := range numGoroutines {
		wg.Go(func() {
			for range numOperationsPerGoroutine {
				// Acquire
				if err := guard.TryAcquireMethod("TestMethod"); err != nil {
					errors[idx] = err
					return
				}

				// Small sleep to increase chance of race conditions
				time.Sleep(time.Microsecond)

				// Release
				guard.ReleaseMethod("TestMethod")
			}
		})
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
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 1,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)
		interceptor := ConcurrencyInterceptor(guard, nil, nil)

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
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 1,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)
		interceptor := ConcurrencyInterceptor(guard, nil, nil)

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
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 10,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)
		interceptor := ConcurrencyInterceptor(guard, nil, nil)

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
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 10,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)
		interceptor := ConcurrencyInterceptor(guard, nil, nil)

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
		interceptor := ConcurrencyInterceptor(limiter, nil, nil)

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

type spyGuard struct {
	tryCount     int
	releaseCount int
	failAcquire  bool
}

func (s *spyGuard) TryAcquireMethod(string) error {
	s.tryCount++
	if s.failAcquire {
		return status.Errorf(codes.ResourceExhausted, "should not be called")
	}
	return nil
}

func (s *spyGuard) ReleaseMethod(string) {
	s.releaseCount++
}

func TestConcurrencyInterceptor_ExcludedIP_BypassesGuard(t *testing.T) {
	// Exclude this IP via knob
	excludedIP := "203.0.113.10"
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyExcludeIps, excludedIP): 1,
	})
	guard := &spyGuard{failAcquire: true}
	provider := NewGRPCClientInfoProvider(0)
	interceptor := ConcurrencyInterceptor(guard, provider, mockKnobs)

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}
	// Put the client IP in the peer context so provider can read it.
	ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(excludedIP), Port: 12345}})

	resp, err := interceptor(ctx, nil, info, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
	// Ensure guard was not invoked
	assert.Equal(t, 0, guard.tryCount)
	assert.Equal(t, 0, guard.releaseCount)
}

func TestConcurrencyInterceptor_NonExcludedIP_EnforcesGuard(t *testing.T) {
	// Non-excluded IP should enforce guard; no exclude knob set for this IP.
	clientIP := "198.51.100.55"
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
	guard := &spyGuard{}
	provider := NewGRPCClientInfoProvider(0)
	interceptor := ConcurrencyInterceptor(guard, provider, mockKnobs)

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}
	ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(clientIP), Port: 12345}})

	resp, err := interceptor(ctx, nil, info, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	// Ensure guard was invoked and released once
	assert.Equal(t, 1, guard.tryCount)
	assert.Equal(t, 1, guard.releaseCount)
}

func TestConcurrencyInterceptor_ExcludedPubkey_BypassesGuard(t *testing.T) {
	// Generate a test identity pubkey hex and exclude it via knob
	priv := keys.GeneratePrivateKey()
	identityHex := priv.Public().ToHex()

	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyExcludePubkeys, identityHex): 1,
	})
	guard := &spyGuard{failAcquire: true}
	interceptor := ConcurrencyInterceptor(guard, nil, mockKnobs)

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}
	// Context with identity only
	ctx := authn.InjectSessionForTests(t.Context(), identityHex, time.Now().Add(time.Hour).Unix())

	resp, err := interceptor(ctx, nil, info, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
	// Ensure guard was not invoked
	assert.Equal(t, 0, guard.tryCount)
	assert.Equal(t, 0, guard.releaseCount)
}

func TestConcurrencyGuard_AcquireAfterGlobalLimit(t *testing.T) {
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "global"): 3,
	})
	guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)

	// Acquire some resources
	for range 3 {
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

func TestConcurrencyGuard_AcquireGlobalStreamLimit(t *testing.T) {
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_UnaryGlobalLimit):  3,
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_StreamGlobalLimit): 5,
	})
	guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_UnaryGlobalLimit)
	guardStream := NewConcurrencyGuard(mockKnobs, KnobTargetName_StreamGlobalLimit)

	// Acquire some resources
	for range 3 {
		err := guard.TryAcquireMethod("TestMethod")
		require.NoError(t, err)
	}

	// Verify current count
	concurrencyGuard := guard.(*ConcurrencyGuard)
	assert.Equal(t, int64(3), concurrencyGuard.counterMap["TestMethod"])

	// Acquiring again fails
	err := guard.TryAcquireMethod("TestMethod")
	require.Error(t, err)

	// Acquire some streamresources
	for range 5 {
		err := guardStream.TryAcquireMethod("TestMethod")
		require.NoError(t, err)
	}

	// Verify current count
	concurrencyGuardStream := guardStream.(*ConcurrencyGuard)
	assert.Equal(t, int64(5), concurrencyGuardStream.counterMap["TestMethod"])

	// Acquiring again fails
	err = guardStream.TryAcquireMethod("TestMethod")
	require.Error(t, err)

	// Method counter is still at 5
	assert.Equal(t, int64(5), concurrencyGuardStream.counterMap["TestMethod"])

	guard.ReleaseMethod("TestMethod")

	// Global counter is decremented
	assert.Equal(t, int64(2), concurrencyGuard.globalCounter)

	// Acquiring after release works
	err = guard.TryAcquireMethod("TestMethod")
	require.NoError(t, err)

	// But stream guard is still at 5
	err = guardStream.TryAcquireMethod("TestMethod")
	require.Error(t, err)

	guardStream.ReleaseMethod("TestMethod")

	// Global counter is decremented
	assert.Equal(t, int64(4), concurrencyGuardStream.globalCounter)

	// Acquiring after release works
	err = guardStream.TryAcquireMethod("TestMethod")
	require.NoError(t, err)
}

// mockServerStream is a minimal mock implementation of grpc.ServerStream for testing.
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

func TestConcurrencyStreamInterceptor(t *testing.T) {
	t.Run("successful request within limit", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_StreamGlobalLimit): 1,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_StreamGlobalLimit)
		interceptor := ConcurrencyStreamInterceptor(guard, nil, nil)

		called := false
		handler := func(srv any, stream grpc.ServerStream) error {
			called = true
			return nil
		}

		info := &grpc.StreamServerInfo{
			FullMethod: "/test.Service/TestStream",
		}
		ss := &mockServerStream{ctx: t.Context()}

		err := interceptor(nil, ss, info, handler)

		require.NoError(t, err)
		assert.True(t, called)

		// Verify resource was released
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["/test.Service/TestStream"])
		assert.Equal(t, int64(0), concurrencyGuard.globalCounter)
	})

	t.Run("request exceeding limit", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_StreamGlobalLimit): 1,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_StreamGlobalLimit)
		interceptor := ConcurrencyStreamInterceptor(guard, nil, nil)

		// First acquire the only slot
		err := guard.TryAcquireMethod("/test.Service/TestStream")
		require.NoError(t, err)

		called := false
		handler := func(srv any, stream grpc.ServerStream) error {
			called = true
			return nil
		}

		info := &grpc.StreamServerInfo{
			FullMethod: "/test.Service/TestStream",
		}
		ss := &mockServerStream{ctx: t.Context()}

		err = interceptor(nil, ss, info, handler)

		require.Error(t, err)
		assert.False(t, called)

		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.ResourceExhausted, st.Code())
		assert.Contains(t, err.Error(), "concurrency limit exceeded")
	})

	t.Run("handler panic still releases resource", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_StreamGlobalLimit): 10,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_StreamGlobalLimit)
		interceptor := ConcurrencyStreamInterceptor(guard, nil, nil)

		handler := func(srv any, stream grpc.ServerStream) error {
			panic("test panic")
		}

		info := &grpc.StreamServerInfo{
			FullMethod: "/test.Service/TestStream",
		}
		ss := &mockServerStream{ctx: t.Context()}

		// Should panic but still release the resource
		assert.Panics(t, func() {
			_ = interceptor(nil, ss, info, handler)
		})

		// Verify resource was released despite panic
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["/test.Service/TestStream"])
		assert.Equal(t, int64(0), concurrencyGuard.globalCounter)
	})

	t.Run("handler error still releases resource", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_StreamGlobalLimit): 10,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_StreamGlobalLimit)
		interceptor := ConcurrencyStreamInterceptor(guard, nil, nil)

		expectedErr := fmt.Errorf("handler error")
		handler := func(srv any, stream grpc.ServerStream) error {
			return expectedErr
		}

		info := &grpc.StreamServerInfo{
			FullMethod: "/test.Service/TestStream",
		}
		ss := &mockServerStream{ctx: t.Context()}

		err := interceptor(nil, ss, info, handler)

		require.Error(t, err)
		assert.Equal(t, expectedErr, err)

		// Verify resource was released
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Equal(t, int64(0), concurrencyGuard.counterMap["/test.Service/TestStream"])
		assert.Equal(t, int64(0), concurrencyGuard.globalCounter)
	})

	t.Run("with noop limiter", func(t *testing.T) {
		limiter := &NoopResourceLimiter{}
		interceptor := ConcurrencyStreamInterceptor(limiter, nil, nil)

		called := false
		handler := func(srv any, stream grpc.ServerStream) error {
			called = true
			return nil
		}

		info := &grpc.StreamServerInfo{
			FullMethod: "/test.Service/TestStream",
		}
		ss := &mockServerStream{ctx: t.Context()}

		err := interceptor(nil, ss, info, handler)

		require.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("method limit exceeded", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, "/test.Service/TestStream"):       2,
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_StreamGlobalLimit): 10,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_StreamGlobalLimit)
		interceptor := ConcurrencyStreamInterceptor(guard, nil, nil)

		// Acquire two slots (the method limit)
		require.NoError(t, guard.TryAcquireMethod("/test.Service/TestStream"))
		require.NoError(t, guard.TryAcquireMethod("/test.Service/TestStream"))

		called := false
		handler := func(srv any, stream grpc.ServerStream) error {
			called = true
			return nil
		}

		info := &grpc.StreamServerInfo{
			FullMethod: "/test.Service/TestStream",
		}
		ss := &mockServerStream{ctx: t.Context()}

		err := interceptor(nil, ss, info, handler)

		require.Error(t, err)
		assert.False(t, called)

		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.ResourceExhausted, st.Code())
	})

	t.Run("concurrent streams respect limit", func(t *testing.T) {
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyLimitLimit, KnobTargetName_StreamGlobalLimit): 5,
		})
		guard := NewConcurrencyGuard(mockKnobs, KnobTargetName_StreamGlobalLimit)
		interceptor := ConcurrencyStreamInterceptor(guard, nil, nil)

		numGoroutines := 10
		var wg sync.WaitGroup
		successCount := 0
		failureCount := 0
		var mu sync.Mutex

		// Use a channel to synchronize all handlers
		handlerStarted := make(chan struct{}, numGoroutines)
		handlerComplete := make(chan struct{})

		for range numGoroutines {
			wg.Go(func() {
				handler := func(srv any, stream grpc.ServerStream) error {
					handlerStarted <- struct{}{}
					<-handlerComplete // Wait for signal to complete
					return nil
				}

				info := &grpc.StreamServerInfo{
					FullMethod: "/test.Service/TestStream",
				}
				ss := &mockServerStream{ctx: t.Context()}

				err := interceptor(nil, ss, info, handler)

				mu.Lock()
				if err != nil {
					failureCount++
				} else {
					successCount++
				}
				mu.Unlock()
			})
		}
		time.Sleep(10 * time.Millisecond)
		concurrencyGuard := guard.(*ConcurrencyGuard)
		assert.Positive(t, concurrencyGuard.globalCounter)

		// Wait for all handlers that can start to start
		time.Sleep(100 * time.Millisecond)

		// Signal all handlers to complete
		close(handlerComplete)

		wg.Wait()

		// With a limit of 5 and 10 concurrent requests, 5 should succeed and 5 should fail
		assert.Equal(t, 5, successCount)
		assert.Equal(t, 5, failureCount)

		// Verify all resources were released
		assert.Equal(t, int64(0), concurrencyGuard.globalCounter)
	})
}

func TestConcurrencyStreamInterceptor_ExcludedIP_BypassesGuard(t *testing.T) {
	// Exclude this IP via knob
	excludedIP := "203.0.113.10"
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyExcludeIps, excludedIP): 1,
	})
	guard := &spyGuard{failAcquire: true}
	provider := NewGRPCClientInfoProvider(0)
	interceptor := ConcurrencyStreamInterceptor(guard, provider, mockKnobs)

	called := false
	handler := func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/TestStream"}
	// Put the client IP in the peer context so provider can read it.
	ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(excludedIP), Port: 12345}})
	ss := &mockServerStream{ctx: ctx}

	err := interceptor(nil, ss, info, handler)
	require.NoError(t, err)
	assert.True(t, called)
	// Ensure guard was not invoked
	assert.Equal(t, 0, guard.tryCount)
	assert.Equal(t, 0, guard.releaseCount)
}

func TestConcurrencyStreamInterceptor_NonExcludedIP_EnforcesGuard(t *testing.T) {
	// Non-excluded IP should enforce guard; no exclude knob set for this IP.
	clientIP := "198.51.100.55"
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
	guard := &spyGuard{}
	provider := NewGRPCClientInfoProvider(0)
	interceptor := ConcurrencyStreamInterceptor(guard, provider, mockKnobs)

	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/TestStream"}
	ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(clientIP), Port: 12345}})
	ss := &mockServerStream{ctx: ctx}

	err := interceptor(nil, ss, info, handler)
	require.NoError(t, err)
	// Ensure guard was invoked and released once
	assert.Equal(t, 1, guard.tryCount)
	assert.Equal(t, 1, guard.releaseCount)
}

func TestConcurrencyStreamInterceptor_ExcludedPubkey_BypassesGuard(t *testing.T) {
	// Generate a test identity pubkey hex and exclude it via knob
	priv := keys.GeneratePrivateKey()
	identityHex := priv.Public().ToHex()

	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		fmt.Sprintf("%s@%s", knobs.KnobGrpcServerConcurrencyExcludePubkeys, identityHex): 1,
	})
	guard := &spyGuard{failAcquire: true}
	interceptor := ConcurrencyStreamInterceptor(guard, nil, mockKnobs)

	called := false
	handler := func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/TestStream"}
	// Context with identity only
	ctx := authn.InjectSessionForTests(t.Context(), identityHex, time.Now().Add(time.Hour).Unix())
	ss := &mockServerStream{ctx: ctx}

	err := interceptor(nil, ss, info, handler)
	require.NoError(t, err)
	assert.True(t, called)
	// Ensure guard was not invoked
	assert.Equal(t, 0, guard.tryCount)
	assert.Equal(t, 0, guard.releaseCount)
}
