package middleware

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type testClock struct {
	Time time.Time
}

func (c *testClock) Now() time.Time {
	return c.Time
}

type testMemoryStore struct {
	clock     Clock
	buckets   map[string]*testBucket
	bucketsMu sync.RWMutex
}

type testBucket struct {
	tokens      uint64
	window      time.Duration
	windowStart time.Time // When current window started
	remaining   uint64
}

func newTestMemoryStore(clock Clock) *testMemoryStore {
	store := &testMemoryStore{
		clock:   clock,
		buckets: make(map[string]*testBucket),
	}
	return store
}

func (s *testMemoryStore) Get(ctx context.Context, key string) (tokens uint64, remaining uint64, err error) {
	s.bucketsMu.RLock()
	defer s.bucketsMu.RUnlock()

	bucket, exists := s.buckets[key]
	if !exists {
		return 0, 0, nil
	}
	return bucket.tokens, bucket.remaining, nil
}

func (s *testMemoryStore) Set(ctx context.Context, key string, tokens uint64, window time.Duration) error {
	s.bucketsMu.Lock()
	defer s.bucketsMu.Unlock()

	now := s.clock.Now()
	s.buckets[key] = &testBucket{
		tokens:      tokens,
		window:      window,
		windowStart: now,
		remaining:   tokens,
	}
	return nil
}

func (s *testMemoryStore) Take(ctx context.Context, key string) (tokens uint64, remaining uint64, reset uint64, ok bool, err error) {
	s.bucketsMu.Lock()
	defer s.bucketsMu.Unlock()

	bucket, exists := s.buckets[key]
	if !exists {
		return 0, 0, 0, false, nil
	}

	now := s.clock.Now()

	// Check if current window has expired and we need to start a new window
	elapsed := now.Sub(bucket.windowStart)
	if elapsed >= bucket.window {
		// Start new window
		bucket.windowStart = now
		bucket.remaining = bucket.tokens
	}

	// Calculate when next reset will happen
	nextReset := bucket.windowStart.Add(bucket.window)

	if bucket.remaining > 0 {
		bucket.remaining--
		return bucket.tokens, bucket.remaining, uint64(nextReset.Unix()), true, nil
	}

	return bucket.tokens, 0, uint64(nextReset.Unix()), false, nil
}

func TestRateLimiter(t *testing.T) {

	t.Run("basic rate limiting", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})

	t.Run("per-method limits allow dynamic updates", func(t *testing.T) {
		knobValues := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Method1#1s": 5,
			knobs.KnobRateLimitLimit + "@/test.Service/Method2#1s": 1,
		}
		mockKnobs := knobs.NewFixedKnobs(knobValues)

		config := &RateLimiterConfig{}

		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}

		mockStore := newTestMemoryStore(clock)
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(mockStore), WithClock(clock))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))

		// Test Method1 with custom limit of 5 requests
		info1 := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method1"}

		// First 5 requests should succeed
		for i := 0; i < 5; i++ {
			resp, err := interceptor(ctx, "request", info1, handler)
			require.NoError(t, err, "Method1 request %d should succeed", i+1)
			assert.Equal(t, "ok", resp)
		}

		// 6th request should fail due to rate limit
		_, err = interceptor(ctx, "request", info1, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))

		// But if we dynamically update the knob value for this method, it
		// should work again.
		knobValues[knobs.KnobRateLimitLimit+"@/test.Service/Method1"] = 50
		clock.Time = clock.Time.Add(2 * time.Second)
		resp, err := interceptor(ctx, "request", info1, handler)
		require.NoError(t, err, "Method1 request should succeed after knob update")
		assert.Equal(t, "ok", resp)

		// Test Method2 with custom limit of 1 request
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "5.6.7.8",
		}))
		info2 := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method2"}

		// First request should succeed
		resp, err = interceptor(ctx2, "request", info2, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		// 2nd request should fail due to rate limit
		_, err = interceptor(ctx2, "request", info2, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))

	})

	// Service-level dynamic update: verify service bucket applies across methods and updates live
	t.Run("service-level limits allow dynamic updates", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		knobValues := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/#1s": 2,
		}
		mockKnobs := knobs.NewFixedKnobs(knobValues)

		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)
		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

		// Method A under service limit=2
		infoA := &grpc.UnaryServerInfo{FullMethod: "/test.Service/MethodA"}
		_, err = interceptor(ctx, "request", infoA, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", infoA, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", infoA, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Advance window and update service limit to 3
		clock.Time = clock.Time.Add(2 * time.Second)
		knobValues[knobs.KnobRateLimitLimit+"@/test.Service/#1s"] = 3

		// Method B should now be limited by 3 in new window
		infoB := &grpc.UnaryServerInfo{FullMethod: "/test.Service/MethodB"}
		for i := 0; i < 3; i++ {
			_, err := interceptor(ctx, "request", infoB, handler)
			require.NoError(t, err)
		}
		_, err = interceptor(ctx, "request", infoB, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	// Global-level dynamic update: verify global bucket updates live and applies to all methods
	t.Run("global limits allow dynamic updates", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		knobValues := map[string]float64{
			knobs.KnobRateLimitLimit + "@global#1s": 2,
		}
		mockKnobs := knobs.NewFixedKnobs(knobValues)

		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)
		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Any"}

		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Advance and update global to 3
		clock.Time = clock.Time.Add(2 * time.Second)
		knobValues[knobs.KnobRateLimitLimit+"@global#1s"] = 3

		for i := 0; i < 3; i++ {
			_, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
		}
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	t.Run("service-level limit applies to all methods in service", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/#1s": 2,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/AnyMethod"}
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		serviceKey := "rl:/test.Service/#1s:1.2.3.4"
		store.bucketsMu.RLock()
		bucket, exists := store.buckets[serviceKey]
		store.bucketsMu.RUnlock()
		require.True(t, exists)
		assert.Equal(t, uint64(2), bucket.tokens)
		assert.Equal(t, uint64(0), bucket.remaining)
	})

	t.Run("global limit applies to all methods", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@global#1s": 2,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		globalKey := "rl:global#1s:1.2.3.4"
		store.bucketsMu.RLock()
		bucket, exists := store.buckets[globalKey]
		store.bucketsMu.RUnlock()
		require.True(t, exists)
		assert.Equal(t, uint64(2), bucket.tokens)
		assert.Equal(t, uint64(0), bucket.remaining)
	})

	t.Run("method and global both enforced per tier", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Method#1s": 2,
			knobs.KnobRateLimitLimit + "@global#1s":               3,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

		// Two requests succeed; third fails due to per-method bucket
		for i := 0; i < 2; i++ {
			_, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
		}
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		methodKey := "rl:/test.Service/Method#1s:1.2.3.4"
		globalKey := "rl:global#1s:1.2.3.4"
		store.bucketsMu.RLock()
		mb, mExists := store.buckets[methodKey]
		gb, gExists := store.buckets[globalKey]
		store.bucketsMu.RUnlock()
		require.True(t, mExists)
		require.True(t, gExists)
		assert.Equal(t, uint64(2), mb.tokens)
		assert.Equal(t, uint64(0), mb.remaining)
		assert.Equal(t, uint64(3), gb.tokens)
		assert.Equal(t, uint64(1), gb.remaining)
	})

	t.Run("service and global both enforced per tier", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/#1s": 2,
			knobs.KnobRateLimitLimit + "@global#1s":         3,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

		for i := 0; i < 2; i++ {
			_, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
		}
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		serviceKey := "rl:/test.Service/#1s:1.2.3.4"
		globalKey := "rl:global#1s:1.2.3.4"
		store.bucketsMu.RLock()
		sb, sExists := store.buckets[serviceKey]
		gb, gExists := store.buckets[globalKey]
		store.bucketsMu.RUnlock()
		require.True(t, sExists)
		require.True(t, gExists)
		assert.Equal(t, uint64(2), sb.tokens)
		assert.Equal(t, uint64(0), sb.remaining)
		assert.Equal(t, uint64(3), gb.tokens)
		assert.Equal(t, uint64(1), gb.remaining)
	})

	t.Run("method, service, and global enforced per tier", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Method#1s": 1,
			knobs.KnobRateLimitLimit + "@/test.Service/#1s":       2,
			knobs.KnobRateLimitLimit + "@global#1s":               3,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		methodKey := "rl:/test.Service/Method#1s:1.2.3.4"
		serviceKey := "rl:/test.Service/#1s:1.2.3.4"
		globalKey := "rl:global#1s:1.2.3.4"
		store.bucketsMu.RLock()
		mb, mExists := store.buckets[methodKey]
		sb, sExists := store.buckets[serviceKey]
		gb, gExists := store.buckets[globalKey]
		store.bucketsMu.RUnlock()
		require.True(t, mExists)
		require.True(t, sExists)
		require.True(t, gExists)
		assert.Equal(t, uint64(1), mb.tokens)
		assert.Equal(t, uint64(0), mb.remaining)
		assert.Equal(t, uint64(2), sb.tokens)
		assert.Equal(t, uint64(1), sb.remaining)
		assert.Equal(t, uint64(3), gb.tokens)
		assert.Equal(t, uint64(2), gb.remaining)
	})

	t.Run("method not rate limited", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/NotLimited"}

		for i := 0; i < 5; i++ {
			resp, err := interceptor(t.Context(), "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
	})

	t.Run("window expiration", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithClock(clock), WithStore(store), WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))

		// First 2 requests should succeed (config.MaxRequests = 2)
		for i := 0; i < 2; i++ {
			resp, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err, "Request %d should succeed", i+1)
			assert.Equal(t, "ok", resp)
		}

		// 3rd request should fail due to rate limit
		_, err = interceptor(ctx, "request", info, handler)
		require.Error(t, err)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		assert.Contains(t, status.Convert(err).Message(), "rate limit exceeded")

		// Now simulate time passing which resets the rate limit (config.Window = 1 second)
		clock.Time = clock.Time.Add(2 * time.Second)

		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)
	})

	t.Run("different clients", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		ctx1 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "5.6.7.8",
		}))

		resp, err := interceptor(ctx1, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx1, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctx1, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))

		_, err = interceptor(ctx2, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})

	t.Run("multiple x-forwarded-for headers", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Create metadata with multiple x-forwarded-for headers
		md := metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4, 5.6.7.8, 9.10.11.12",
		})
		md2 := metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4, 5.6.7.8, 9.10.11.13",
		})
		ctx := metadata.NewIncomingContext(t.Context(), md)
		ctx2 := metadata.NewIncomingContext(t.Context(), md2)

		// Should use the last IP (9.10.11.12) for rate limiting, so exhaust the
		// resources with the first two requests, but then make sure the third
		// request goes through.
		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))

		resp, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)
	})

	t.Run("x-real-ip ignored", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Create metadata with only x-real-ip (no x-forwarded-for)
		md := metadata.New(map[string]string{
			"x-real-ip": "1.2.3.4",
		})
		ctx := metadata.NewIncomingContext(t.Context(), md)

		// Should not rate limit since x-real-ip is ignored
		for i := 0; i < 5; i++ {
			resp, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
	})

	t.Run("custom x-forwarded-for client IP position", func(t *testing.T) {
		// Configure rate limiter to use the second-to-last IP (position 1)
		configWithCustomPosition := &RateLimiterConfig{XffClientIpPosition: 1}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(configWithCustomPosition, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Create metadata with multiple x-forwarded-for headers
		// Format: "client,proxy1,proxy2" - using position 1 should use "proxy1"
		md := metadata.New(map[string]string{
			"x-forwarded-for": "192.168.1.100, 10.0.0.1, 172.16.0.1",
		})
		ctx := metadata.NewIncomingContext(t.Context(), md)

		// Should use "10.0.0.1" (second-to-last) for rate limiting
		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))

		// Test just switching the second-to-last IP to ensure it isn't rate
		// limited initially even though the prior IP in that position was
		// limited, but then it is rate limited after the limit is exceeded.
		md2 := metadata.New(map[string]string{
			"x-forwarded-for": "192.168.1.100, 10.0.0.2, 172.16.0.1",
		})
		ctx2 := metadata.NewIncomingContext(t.Context(), md2)

		resp, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctx2, "request", info, handler)
		require.Error(t, err)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		assert.Contains(t, status.Convert(err).Message(), "rate limit exceeded")
	})

	t.Run("knob values enforced", func(t *testing.T) {
		config := &RateLimiterConfig{}

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Enable#1s":   2,
			knobs.KnobRateLimitLimit + "@/test.Service/Disable1#1s": 0,
			knobs.KnobRateLimitLimit + "@/test.Service/Disable2#1s": -1,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		tests := []struct {
			name          string
			method        string
			expectedError bool
			requests      int
		}{
			{
				name:          "knob value > 0 enables rate limiting",
				method:        "/test.Service/Enable",
				expectedError: false,
				requests:      2, // Should succeed for first 2 requests
			},
			{
				name:          "knob value > 0 rate limits after max requests",
				method:        "/test.Service/Enable",
				expectedError: true,
				requests:      3, // Third request should fail
			},
			{
				name:          "knob value = 0 disables rate limiting",
				method:        "/test.Service/Disable1",
				expectedError: false,
				requests:      5, // Should allow unlimited requests
			},
			{
				name:          "knob value < 0 overrides config method",
				method:        "/test.Service/Disable2",
				expectedError: false,
				requests:      5, // Should allow unlimited requests despite being in config
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
				require.NoError(t, err)

				interceptor := rateLimiter.UnaryServerInterceptor()
				handler := func(_ context.Context, _ any) (any, error) {
					return "ok", nil
				}

				ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
					"x-forwarded-for": "1.2.3.4",
				}))

				info := &grpc.UnaryServerInfo{FullMethod: tt.method}

				var resp any
				for i := 0; i < tt.requests-1; i++ {
					resp, err = interceptor(ctx, "request", info, handler)
					require.NoError(t, err)
					require.Equal(t, "ok", resp)
				}
				resp, err = interceptor(ctx, "request", info, handler)
				if tt.expectedError {
					require.ErrorContains(t, err, "rate limit exceeded")
					require.Equal(t, codes.ResourceExhausted, status.Code(err))
				} else {
					require.NoError(t, err)
					require.Equal(t, "ok", resp)
				}
			})
		}
	})

	t.Run("per-method max requests knob values are read correctly", func(t *testing.T) {

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Method1#1s": 5,
			knobs.KnobRateLimitLimit + "@/test.Service/Method2#1s": 1,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		method1Key := "/test.Service/Method1#1s"
		method1Value := mockKnobs.GetValueTarget(knobs.KnobRateLimitLimit, &method1Key, 0)
		assert.InDelta(t, 5.0, method1Value, 0.001, "Method1 should have custom limit of 5")

		method2Key := "/test.Service/Method2#1s"
		method2Value := mockKnobs.GetValueTarget(knobs.KnobRateLimitLimit, &method2Key, 0)
		assert.InDelta(t, 1.0, method2Value, 0.001, "Method2 should have custom limit of 1")

		defaultKey := "/test.Service/Default#1s"
		methodDefaultValue := mockKnobs.GetValueTarget(knobs.KnobRateLimitLimit, &defaultKey, 2)
		assert.InDelta(t, 2.0, methodDefaultValue, 0.001, "Default method should use default argument of 2")
	})
	t.Run("tiers enforce limits and windowing via suffix", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		mockStore := newTestMemoryStore(clock)

		config := &RateLimiterConfig{}
		knobValues := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Method1#1s": 2,
			knobs.KnobRateLimitLimit + "@/test.Service/Method1#1m": 3,
		}
		mockKnobs := knobs.NewFixedKnobs(knobValues)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(mockStore), WithClock(clock))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method1"}
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

		// Under both tiers: allow 2 in 1s, 3 in 3s
		for i := 0; i < 2; i++ {
			_, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
		}
		// Third within same second should fail due to #1s tier
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Advance 1s resets #1s tier, but #1m tier still counts
		clock.Time = clock.Time.Add(1 * time.Second)
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		// At this point, within 1m window, total is 3 -> next should fail due to #1m tier
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	t.Run("IP address excluded via knobs", func(t *testing.T) {
		config := &RateLimiterConfig{}

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitExcludeIps + "@1.2.3.4":                1,
			knobs.KnobRateLimitExcludeIps + "@5.6.7.8":                0,
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 2,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// IP 1.2.3.4 is excluded, so it should not be rate-limited.
		ctxExcluded := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		for i := 0; i < 5; i++ {
			resp, err := interceptor(ctxExcluded, "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}

		// IP 5.6.7.8 is not excluded, so it should be rate-limited.
		ctxNotExcluded := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "5.6.7.8",
		}))

		resp, err := interceptor(ctxNotExcluded, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctxNotExcluded, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctxNotExcluded, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})
}
