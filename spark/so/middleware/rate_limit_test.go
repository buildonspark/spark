package middleware

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
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

func newIdentityHex(t *testing.T) string {
	t.Helper()
	priv := keys.GeneratePrivateKey()
	return priv.Public().ToHex()
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
	s.bucketsMu.Lock()
	defer s.bucketsMu.Unlock()

	bucket, exists := s.buckets[key]
	if !exists {
		return 0, 0, nil
	}

	// Check if current window has expired and we need to start a new window
	now := s.clock.Now()
	if elapsed := now.Sub(bucket.windowStart); elapsed >= bucket.window {
		bucket.windowStart = now
		bucket.remaining = bucket.tokens
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

func (s *testMemoryStore) Take(ctx context.Context, key string) (ok bool, err error) {
	s.bucketsMu.Lock()
	defer s.bucketsMu.Unlock()

	bucket, exists := s.buckets[key]
	if !exists {
		return false, nil
	}

	now := s.clock.Now()

	// Check if current window has expired and we need to start a new window
	elapsed := now.Sub(bucket.windowStart)
	if elapsed >= bucket.window {
		// Start new window
		bucket.windowStart = now
		bucket.remaining = bucket.tokens
	}

	if bucket.remaining > 0 {
		bucket.remaining--
		return true, nil
	}

	return false, nil
}

// Delete removes a bucket (used to simulate backend eviction in tests)
func (s *testMemoryStore) Delete(key string) {
	s.bucketsMu.Lock()
	defer s.bucketsMu.Unlock()
	delete(s.buckets, key)
}

type countingStore struct {
	underlying MemoryStore
	mu         sync.Mutex
	setCount   int
}

func newCountingStore(under MemoryStore) *countingStore {
	return &countingStore{underlying: under}
}

func (s *countingStore) Get(ctx context.Context, key string) (tokens uint64, remaining uint64, err error) {
	return s.underlying.Get(ctx, key)
}

func (s *countingStore) Set(ctx context.Context, key string, tokens uint64, window time.Duration) error {
	s.mu.Lock()
	s.setCount++
	s.mu.Unlock()
	return s.underlying.Set(ctx, key, tokens, window)
}

func (s *countingStore) Take(ctx context.Context, key string) (ok bool, err error) {
	return s.underlying.Take(ctx, key)
}

func (s *countingStore) SetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setCount
}

type mutableKnobs struct {
	mu     sync.RWMutex
	values map[string]float64
}

func newMutableKnobs(initial map[string]float64) *mutableKnobs {
	cp := make(map[string]float64, len(initial))
	maps.Copy(cp, initial)
	return &mutableKnobs{values: cp}
}

func (m *mutableKnobs) keyString(knob string, target *string) string {
	if target != nil {
		return fmt.Sprintf("%s@%s", knob, *target)
	}
	return knob
}

func (m *mutableKnobs) Set(key string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
}

func (m *mutableKnobs) GetValueTarget(knob string, target *string, defaultValue float64) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := m.keyString(knob, target)
	if v, ok := m.values[key]; ok {
		return v
	}
	return defaultValue
}

func (m *mutableKnobs) GetValue(knob string, defaultValue float64) float64 {
	return m.GetValueTarget(knob, nil, defaultValue)
}

func (m *mutableKnobs) GetDurationTarget(knob string, target *string, defaultDuration time.Duration) time.Duration {
	seconds := m.GetValueTarget(knob, target, defaultDuration.Seconds())
	if seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return defaultDuration
}

func (m *mutableKnobs) GetDuration(knob string, defaultDuration time.Duration) time.Duration {
	return m.GetDurationTarget(knob, nil, defaultDuration)
}

func (m *mutableKnobs) RolloutRandomTarget(knob string, target *string, defaultValue float64) bool {
	value := m.GetValueTarget(knob, target, defaultValue)
	return value > 0
}

func (m *mutableKnobs) RolloutRandom(knob string, defaultValue float64) bool {
	return m.RolloutRandomTarget(knob, nil, defaultValue)
}

func (m *mutableKnobs) RolloutUUIDTarget(knob string, _ uuid.UUID, target *string, defaultValue float64) bool {
	value := m.GetValueTarget(knob, target, defaultValue)
	return value > 0
}

func (m *mutableKnobs) RolloutUUID(knob string, _ uuid.UUID, defaultValue float64) bool {
	return m.RolloutRandomTarget(knob, nil, defaultValue)
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

	t.Run("telemetry for limits and utilization on non-exceeded request", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		ip := "9.9.9.9"
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			// method
			knobs.KnobRateLimitLimit + "@/test.Service/Method:ip#1s":     2,
			knobs.KnobRateLimitLimit + "@/test.Service/Method:pubkey#1s": 3,
			// service
			knobs.KnobRateLimitLimit + "@/test.Service/:ip#1s":     5,
			knobs.KnobRateLimitLimit + "@/test.Service/:pubkey#1s": 7,
			// global
			knobs.KnobRateLimitLimit + "@global:ip#1s":     11,
			knobs.KnobRateLimitLimit + "@global:pubkey#1s": 13,
		})
		reader := msdk.NewManualReader()
		provider := msdk.NewMeterProvider(msdk.WithReader(reader))
		otel.SetMeterProvider(provider)

		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": ip,
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)

		// Collect metrics snapshot
		var rm md.ResourceMetrics
		require.NoError(t, reader.Collect(t.Context(), &rm))
		// Find our histogram and verify 6 data points with expected values
		foundUtil := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "rpc.server.ratelimit_utilization" {
					foundUtil = true
					// Expect histogram points with utilization for each scope/dimension
					require.IsType(t, md.Histogram[float64]{}, m.Data)
					hs, _ := m.Data.(md.Histogram[float64])
					// Sum of counts across all histograms should be 6
					count := 0
					for _, dp := range hs.DataPoints {
						count += int(dp.Count)
					}
					assert.Equal(t, 6, count)
				}
			}
		}
		assert.True(t, foundUtil, "utilization histogram not found")
	})

	t.Run("telemetry for utilization and breach on exceeded request", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		ip := "1.1.1.1"
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			// Only configure method ip at #1s with capacity 1 to isolate behavior
			knobs.KnobRateLimitLimit + "@/test.Service/Method:ip#1s": 1,
		})

		// Setup in-memory OTel reader and meter provider to capture metrics BEFORE constructing the rate limiter
		reader := msdk.NewManualReader()
		provider := msdk.NewMeterProvider(msdk.WithReader(reader))
		otel.SetMeterProvider(provider)
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": ip,
		}))

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

		// First request should pass and record utilization 1.0 (capacity 1, remaining 0)
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		// Collect and assert there's exactly 1 utilization datapoint with value 1.0
		var rm md.ResourceMetrics
		require.NoError(t, reader.Collect(t.Context(), &rm))
		utilCount := 0
		breachCount := 0
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch m.Name {
				case "rpc.server.ratelimit_utilization":
					require.IsType(t, md.Histogram[float64]{}, m.Data)
					hs, _ := m.Data.(md.Histogram[float64])
					for _, dp := range hs.DataPoints {
						utilCount += int(dp.Count)
					}
				case "rpc.server.ratelimit_exceeded_total":
					require.IsType(t, md.Sum[int64]{}, m.Data)
					cn, _ := m.Data.(md.Sum[int64])
					for _, dp := range cn.DataPoints {
						breachCount += int(dp.Value)
					}
				}
			}
		}
		assert.Equal(t, 1, utilCount)
		assert.Equal(t, 0, breachCount)

		// Second request should be ResourceExhausted and must not add a new utilization record
		// When a request exceeds the limit, utilization should not be recorded for that attempt
		_, err = interceptor(ctx, "request", info, handler)
		require.Error(t, err)
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
		// Collect a fresh snapshot and assert utilization count still 1 and breach counter incremented
		rm = md.ResourceMetrics{}
		require.NoError(t, reader.Collect(t.Context(), &rm))
		utilCount = 0
		breachCount = 0
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch m.Name {
				case "rpc.server.ratelimit_utilization":
					require.IsType(t, md.Histogram[float64]{}, m.Data)
					hs, _ := m.Data.(md.Histogram[float64])
					for _, dp := range hs.DataPoints {
						utilCount += int(dp.Count)
					}
				case "rpc.server.ratelimit_exceeded_total":
					require.IsType(t, md.Sum[int64]{}, m.Data)
					cn, _ := m.Data.(md.Sum[int64])
					for _, dp := range cn.DataPoints {
						breachCount += int(dp.Value)
					}
				}
			}
		}
		assert.Equal(t, 1, utilCount)
		assert.Equal(t, 1, breachCount)
	})

	t.Run("method :ip and :pubkey limits enforced independently", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		ip := "8.8.8.8"
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Method:ip#1s":     2,
			knobs.KnobRateLimitLimit + "@/test.Service/Method:pubkey#1s": 1,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		// Context with both pubkey and IP
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": ip,
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

		// First request consumes 1 pubkey token (limit 1) and 1 of 2 IP tokens
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)

		// Second request should fail due to pubkey limit reached, even though IP still has tokens
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Changing the pubkey (same IP) should allow one more request, then fail due to IP limit
		identityHex2 := newIdentityHex(t)
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": ip}))
		ctx2 = authn.InjectSessionForTests(ctx2, identityHex2, time.Now().Add(time.Hour).Unix())
		_, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		identityHex3 := newIdentityHex(t)
		ctx3 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": ip}))
		ctx3 = authn.InjectSessionForTests(ctx3, identityHex3, time.Now().Add(time.Hour).Unix())
		_, err = interceptor(ctx3, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	t.Run("service :ip and :pubkey limits enforced independently", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		ip := "7.7.7.7"
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/:ip#1s":     2,
			knobs.KnobRateLimitLimit + "@/test.Service/:pubkey#1s": 1,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		// Context with both pubkey and IP
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": ip,
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/AnyMethod"}

		// First request consumes 1 pubkey token (limit 1) and 1 of 2 IP tokens (service scope)
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)

		// Second request should fail due to pubkey limit reached, even though IP still has tokens
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Changing the pubkey (same IP) should allow one more request, then fail due to IP service limit
		identityHex2 := newIdentityHex(t)
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": ip}))
		ctx2 = authn.InjectSessionForTests(ctx2, identityHex2, time.Now().Add(time.Hour).Unix())
		_, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		identityHex3 := newIdentityHex(t)
		ctx3 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": ip}))
		ctx3 = authn.InjectSessionForTests(ctx3, identityHex3, time.Now().Add(time.Hour).Unix())
		_, err = interceptor(ctx3, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	t.Run("global :ip and :pubkey limits enforced independently", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		ip := "7.7.7.7"
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@global:ip#1s":     2,
			knobs.KnobRateLimitLimit + "@global:pubkey#1s": 1,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		// Context with both pubkey and IP
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": ip,
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/AnyMethod"}

		// First request consumes 1 pubkey token (limit 1) and 1 of 2 IP tokens (service scope)
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)

		// Second request should fail due to pubkey limit reached, even though IP still has tokens
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Changing the pubkey (same IP) should allow one more request, then fail due to IP service limit
		identityHex2 := newIdentityHex(t)
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": ip}))
		ctx2 = authn.InjectSessionForTests(ctx2, identityHex2, time.Now().Add(time.Hour).Unix())
		_, err = interceptor(ctx2, "request", info, handler)
		require.NoError(t, err)
		identityHex3 := newIdentityHex(t)
		ctx3 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": ip}))
		ctx3 = authn.InjectSessionForTests(ctx3, identityHex3, time.Now().Add(time.Hour).Unix())
		_, err = interceptor(ctx3, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
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
		for i := range 5 {
			resp, err := interceptor(ctx, "request", info1, handler)
			require.NoErrorf(t, err, "Method1 request %d should succeed", i+1)
			assert.Equal(t, "ok", resp)
		}

		// 6th request should fail due to rate limit
		_, err = interceptor(ctx, "request", info1, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))

		// But if we dynamically update the knob value for this method, it
		// should work again.
		knobValues[knobs.KnobRateLimitLimit+"@/test.Service/Method1#1s"] = 50
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
		for range 3 {
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

		for range 3 {
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

		// A different method in the same service should also be limited within the same window
		infoB := &grpc.UnaryServerInfo{FullMethod: "/test.Service/OtherMethod"}
		_, err = interceptor(ctx, "request", infoB, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
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
		// A different method should also be limited due to global limit
		info2 := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Another"}
		_, err = interceptor(ctx, "request", info2, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
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
		for range 2 {
			_, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
		}
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Different method should be allowed once more due to higher global limit, then fail
		info2 := &grpc.UnaryServerInfo{FullMethod: "/test.Service/OtherMethod"}
		_, err = interceptor(ctx, "request", info2, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info2, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
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

		// Another method in the same service should allow one request, then be limited by service scope
		infoOther := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Other"}
		_, err = interceptor(ctx, "request", infoOther, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", infoOther, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// A method in a different service should allow one request, then be limited by global scope
		infoOtherService := &grpc.UnaryServerInfo{FullMethod: "/other.Service/Method"}
		_, err = interceptor(ctx, "request", infoOtherService, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", infoOtherService, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
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

		for range 5 {
			resp, err := interceptor(t.Context(), "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
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
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4, 5.6.7.8, 9.10.11.12",
		}))
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4, 5.6.7.8, 9.10.11.13",
		}))

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
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-real-ip": "1.2.3.4",
		}))

		// Should not rate limit since x-real-ip is ignored
		for range 5 {
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
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "192.168.1.100, 10.0.0.1, 172.16.0.1",
		}))

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
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "192.168.1.100, 10.0.0.2, 172.16.0.1",
		}))

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
		for range 2 {
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

		// At this point, total requests within the 1-minute window is 3. The next request should fail.
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
		for range 5 {
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

	t.Run("Pubkey excluded via knobs", func(t *testing.T) {
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s": 1,
			knobs.KnobRateLimitExcludePubkeys + "@" + identityHex:     1,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Build context with identity only (no x-forwarded-for so only pubkey dimension would apply)
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		// Should not rate limit due to exclusion
		for range 3 {
			resp, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
	})

	t.Run("IP address excluded via dimension-only exclusion", func(t *testing.T) {
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitExcludeIpsOnly + "@1.2.3.4":                   1,
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s":        2,
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod:ip#1s":     2,
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod:pubkey#1s": 2,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Build context with both IP and pubkey
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		// IP is excluded from IP-based rate limiting, but pubkey limits should still apply
		// Make 2 requests that should succeed (within pubkey limit of 2)
		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		// Third request should fail due to pubkey rate limit (IP exclusion doesn't bypass pubkey limits)
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})

	t.Run("Pubkey excluded via dimension-only exclusion", func(t *testing.T) {
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitExcludePubkeysOnly + "@" + identityHex:        1,
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod#1s":        2,
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod:ip#1s":     2,
			knobs.KnobRateLimitLimit + "@/test.Service/TestMethod:pubkey#1s": 2,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		// Build context with both IP and pubkey
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		// Pubkey is excluded from pubkey-based rate limiting, but IP limits should still apply
		// Make 2 requests that should succeed (within IP limit of 2)
		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		// Third request should fail due to IP rate limit (pubkey exclusion doesn't bypass IP limits)
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})

	t.Run("IP dimension-only exclusion with only IP present", func(t *testing.T) {
		config := &RateLimiterConfig{}

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitExcludeIpsOnly + "@1.2.3.4":            1,
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

		// Build context with only IP (no pubkey)
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))

		// IP is excluded, and no pubkey is present, so rate limiting should be bypassed
		for range 5 {
			resp, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
	})

	t.Run("Pubkey dimension-only exclusion with only pubkey present", func(t *testing.T) {
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitExcludePubkeysOnly + "@" + identityHex: 1,
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

		// Build context with only pubkey (no IP)
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		// Pubkey is excluded, and no IP is present, so rate limiting should be bypassed
		for range 5 {
			resp, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
	})

	t.Run("Full exclusion takes precedence over dimension-only exclusion", func(t *testing.T) {
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)

		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitExcludeIps + "@1.2.3.4":                1, // Full exclusion
			knobs.KnobRateLimitExcludeIpsOnly + "@1.2.3.4":            1, // Dimension-only exclusion
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

		// Build context with both IP and pubkey
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		// Full exclusion should bypass all rate limiting, even if dimension-only exclusion is also set
		for range 5 {
			resp, err := interceptor(ctx, "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
	})

	// Method-name prefix: longest match should be chosen
	t.Run("method-name prefix chooses longest match", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/^Sta:ip#1s":   1,
			knobs.KnobRateLimitLimit + "@/test.Service/^Start:ip#1s": 2,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "9.9.9.9"}))

		// Matches both ^Sta and ^Start; expect longest (^Start) with limit 2
		infoStart := &grpc.UnaryServerInfo{FullMethod: "/test.Service/StartEngine"}
		_, err = interceptor(ctx, "request", infoStart, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", infoStart, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", infoStart, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Matches only ^Sta; expect limit 1
		infoStatus := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Status"}
		_, err = interceptor(ctx, "request", infoStatus, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", infoStatus, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	t.Run("prefix dimension-specific overrides base", func(t *testing.T) {
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/^Foo#1s":        5,
			knobs.KnobRateLimitLimit + "@/test.Service/^Foo:pubkey#1s": 1,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rl.UnaryServerInterceptor()
		handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "8.8.4.4"}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())

		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/FooBar"}
		_, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		_, err = interceptor(ctx, "request", info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})
}

func TestRateLimiter_SetOnlyOnInitAndConfigChange(t *testing.T) {
	clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	baseStore := newTestMemoryStore(clock)
	store := newCountingStore(baseStore)
	config := &RateLimiterConfig{}
	ip := "1.2.3.4"
	method := "/test.Service/Method"
	tier := "#1s"

	key := knobs.KnobRateLimitLimit + "@" + method + ":ip" + tier
	mk := newMutableKnobs(map[string]float64{key: 2})

	rl, err := NewRateLimiter(config, WithKnobs(mk), WithStore(store), WithClock(clock))
	require.NoError(t, err)

	ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
		"x-forwarded-for": ip,
	}))
	interceptor := rl.UnaryServerInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: method}

	// First request initializes bucket and consumes one token
	_, err = interceptor(ctx, "request", info, handler)
	require.NoError(t, err)
	assert.Equal(t, 1, store.SetCount())

	// Second request should not trigger Set again
	_, err = interceptor(ctx, "request", info, handler)
	require.NoError(t, err)
	assert.Equal(t, 1, store.SetCount())

	// Change configured limit; next request should re-apply once
	mk.Set(key, 3)
	_, err = interceptor(ctx, "request", info, handler)
	require.NoError(t, err)
	assert.Equal(t, 2, store.SetCount())

	// Another request, no further Set
	_, err = interceptor(ctx, "request", info, handler)
	require.NoError(t, err)
	assert.Equal(t, 2, store.SetCount())
}

func TestRateLimiter_ReapplyLimitsOnBackendEviction(t *testing.T) {
	clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	baseStore := newTestMemoryStore(clock)
	store := newCountingStore(baseStore)
	config := &RateLimiterConfig{}
	ip := "9.9.9.9"
	method := "/test.Service/Method"
	tier := "#1s"
	knobKey := knobs.KnobRateLimitLimit + "@" + method + ":ip" + tier
	mk := newMutableKnobs(map[string]float64{knobKey: 2})

	rl, err := NewRateLimiter(config, WithKnobs(mk), WithStore(store), WithClock(clock))
	require.NoError(t, err)

	ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
		"x-forwarded-for": ip,
	}))
	interceptor := rl.UnaryServerInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: method}

	// Initialize
	_, err = interceptor(ctx, "request", info, handler)
	require.NoError(t, err)
	assert.Equal(t, 1, store.SetCount())

	// Simulate eviction: delete bucket from underlying store
	bucketKey := fmt.Sprintf("rl:%s%s:%s:%s", method, tier, "ip", ip)
	baseStore.Delete(bucketKey)

	// Next request should re-apply once
	_, err = interceptor(ctx, "request", info, handler)
	require.NoError(t, err)
	assert.Equal(t, 2, store.SetCount())

	// And not repeatedly after
	_, err = interceptor(ctx, "request", info, handler)
	if err != nil {
		// if rate limit hit due to small capacity and multiple calls, it is fine; we only check Set count
		_ = err
	}
	assert.Equal(t, 2, store.SetCount())
}

// Fail-open tests: when the underlying store errors, requests should be allowed.

type flakyStore struct {
	underlying *testMemoryStore
	failSet    bool
	failGet    bool
	failTake   bool
}

func (s *flakyStore) Get(ctx context.Context, key string) (uint64, uint64, error) {
	if s.failGet {
		return 0, 0, fmt.Errorf("store get error")
	}
	return s.underlying.Get(ctx, key)
}

func (s *flakyStore) Set(ctx context.Context, key string, tokens uint64, window time.Duration) error {
	if s.failSet {
		return fmt.Errorf("store set error")
	}
	return s.underlying.Set(ctx, key, tokens, window)
}

func (s *flakyStore) Take(ctx context.Context, key string) (bool, error) {
	if s.failTake {
		return false, fmt.Errorf("store take error")
	}
	return s.underlying.Take(ctx, key)
}

func TestRateLimiter_FailOpen_OnSetError(t *testing.T) {
	clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := &flakyStore{underlying: newTestMemoryStore(clock), failSet: true}
	config := &RateLimiterConfig{}
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobRateLimitLimit + "@/svc.M/Op:ip#1s": 1,
	})

	rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
	require.NoError(t, err)
	interceptor := rl.UnaryServerInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.M/Op"}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

	// Should allow (fail open) even though initial Set fails
	_, err = interceptor(ctx, "req", info, handler)
	require.NoError(t, err)
}

func TestRateLimiter_FailOpen_OnGetError(t *testing.T) {
	clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	base := newTestMemoryStore(clock)
	store := &flakyStore{underlying: base}
	config := &RateLimiterConfig{}
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobRateLimitLimit + "@/svc.M/Op:ip#1s": 2,
	})

	rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
	require.NoError(t, err)
	interceptor := rl.UnaryServerInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.M/Op"}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

	// First call initializes successfully
	_, err = interceptor(ctx, "req", info, handler)
	require.NoError(t, err)

	// Now force Get error; should still allow (fail open)
	store.failGet = true
	_, err = interceptor(ctx, "req", info, handler)
	require.NoError(t, err)
}

func TestRateLimiter_FailOpen_OnTakeError(t *testing.T) {
	clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	base := newTestMemoryStore(clock)
	store := &flakyStore{underlying: base}
	config := &RateLimiterConfig{}
	mockKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobRateLimitLimit + "@/svc.M/Op:ip#1s": 2,
	})

	rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
	require.NoError(t, err)
	interceptor := rl.UnaryServerInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.M/Op"}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))

	// Initialize normally
	_, err = interceptor(ctx, "req", info, handler)
	require.NoError(t, err)

	// Force Take error; should still allow (fail open)
	store.failTake = true
	_, err = interceptor(ctx, "req", info, handler)
	require.NoError(t, err)
}

// mockServerStream implements grpc.ServerStream for testing
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

func TestStreamServerInterceptor(t *testing.T) {
	t.Run("basic rate limiting", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestStream#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error {
			return nil
		}
		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/TestStream"}

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		stream := &mockServerStream{ctx: ctx}

		err = interceptor(nil, stream, info, handler)
		require.NoError(t, err)

		err = interceptor(nil, stream, info, handler)
		require.NoError(t, err)

		err = interceptor(nil, stream, info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})

	t.Run("method :ip and :pubkey limits enforced independently", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		store := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		ip := "8.8.8.8"
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Stream:ip#1s":     2,
			knobs.KnobRateLimitLimit + "@/test.Service/Stream:pubkey#1s": 1,
		})
		rl, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(store), WithClock(clock))
		require.NoError(t, err)

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": ip,
		}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())
		stream := &mockServerStream{ctx: ctx}

		interceptor := rl.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error { return nil }
		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}

		// First request consumes 1 pubkey token (limit 1) and 1 of 2 IP tokens
		err = interceptor(nil, stream, info, handler)
		require.NoError(t, err)

		// Second request should fail due to pubkey limit reached
		err = interceptor(nil, stream, info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	t.Run("different clients", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestStream#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error { return nil }
		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/TestStream"}

		ctx1 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		ctx2 := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "5.6.7.8",
		}))
		stream1 := &mockServerStream{ctx: ctx1}
		stream2 := &mockServerStream{ctx: ctx2}

		err = interceptor(nil, stream1, info, handler)
		require.NoError(t, err)
		err = interceptor(nil, stream1, info, handler)
		require.NoError(t, err)

		err = interceptor(nil, stream2, info, handler)
		require.NoError(t, err)
		err = interceptor(nil, stream2, info, handler)
		require.NoError(t, err)

		err = interceptor(nil, stream1, info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))

		err = interceptor(nil, stream2, info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})

	t.Run("IP address excluded via knobs", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitExcludeIps + "@1.2.3.4":                1,
			knobs.KnobRateLimitExcludeIps + "@5.6.7.8":                0,
			knobs.KnobRateLimitLimit + "@/test.Service/TestStream#1s": 2,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error { return nil }
		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/TestStream"}

		// IP 1.2.3.4 is excluded, so it should not be rate-limited.
		ctxExcluded := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		streamExcluded := &mockServerStream{ctx: ctxExcluded}
		for range 5 {
			err := interceptor(nil, streamExcluded, info, handler)
			require.NoError(t, err)
		}

		// IP 5.6.7.8 is not excluded, so it should be rate-limited.
		ctxNotExcluded := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{
			"x-forwarded-for": "5.6.7.8",
		}))
		streamNotExcluded := &mockServerStream{ctx: ctxNotExcluded}

		err = interceptor(nil, streamNotExcluded, info, handler)
		require.NoError(t, err)
		err = interceptor(nil, streamNotExcluded, info, handler)
		require.NoError(t, err)
		err = interceptor(nil, streamNotExcluded, info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	})

	t.Run("Pubkey excluded via knobs", func(t *testing.T) {
		config := &RateLimiterConfig{}
		identityHex := newIdentityHex(t)
		mockKnobsMap := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/TestStream#1s": 1,
			knobs.KnobRateLimitExcludePubkeys + "@" + identityHex:     1,
		}
		mockKnobs := knobs.NewFixedKnobs(mockKnobsMap)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error { return nil }
		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/TestStream"}

		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{}))
		ctx = authn.InjectSessionForTests(ctx, identityHex, time.Now().Add(time.Hour).Unix())
		stream := &mockServerStream{ctx: ctx}

		// Should not rate limit due to exclusion
		for range 5 {
			err := interceptor(nil, stream, info, handler)
			require.NoError(t, err)
		}
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

		interceptor := rl.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error { return nil }
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))
		stream := &mockServerStream{ctx: ctx}

		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream1"}
		err = interceptor(nil, stream, info, handler)
		require.NoError(t, err)
		err = interceptor(nil, stream, info, handler)
		require.NoError(t, err)

		// A different method should also be limited due to global limit
		info2 := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream2"}
		err = interceptor(nil, stream, info2, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})

	t.Run("method not rate limited bypasses without error", func(t *testing.T) {
		config := &RateLimiterConfig{}
		mockKnobs := knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/LimitedStream#1s": 2,
		})
		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs))
		require.NoError(t, err)

		interceptor := rateLimiter.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error { return nil }
		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/NotLimited"}

		for range 5 {
			err := interceptor(nil, &mockServerStream{ctx: t.Context()}, info, handler)
			require.NoError(t, err)
		}
	})

	t.Run("tiers enforce limits and windowing via suffix", func(t *testing.T) {
		clock := &testClock{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		mockStore := newTestMemoryStore(clock)
		config := &RateLimiterConfig{}
		knobValues := map[string]float64{
			knobs.KnobRateLimitLimit + "@/test.Service/Stream#1s": 2,
			knobs.KnobRateLimitLimit + "@/test.Service/Stream#1m": 3,
		}
		mockKnobs := knobs.NewFixedKnobs(knobValues)

		rateLimiter, err := NewRateLimiter(config, WithKnobs(mockKnobs), WithStore(mockStore), WithClock(clock))
		require.NoError(t, err)

		interceptor := rateLimiter.StreamServerInterceptor()
		handler := func(_ any, _ grpc.ServerStream) error { return nil }
		info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
		ctx := metadata.NewIncomingContext(t.Context(), metadata.New(map[string]string{"x-forwarded-for": "1.2.3.4"}))
		stream := &mockServerStream{ctx: ctx}

		// Under both tiers: allow 2 in 1s, 3 in 1m
		for range 2 {
			err := interceptor(nil, stream, info, handler)
			require.NoError(t, err)
		}
		// Third within same second should fail due to #1s tier
		err = interceptor(nil, stream, info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")

		// Advance 1s resets #1s tier, but #1m tier still counts
		clock.Time = clock.Time.Add(1 * time.Second)
		err = interceptor(nil, stream, info, handler)
		require.NoError(t, err)

		// At this point, total requests within the 1-minute window is 3. The next request should fail.
		err = interceptor(nil, stream, info, handler)
		require.ErrorContains(t, err, "rate limit exceeded")
	})
}
