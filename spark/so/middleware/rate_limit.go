package middleware

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/sethvargo/go-limiter"
	"github.com/sethvargo/go-limiter/memorystore"
	"google.golang.org/grpc"
)

// sanitizeKey removes control characters and limits key length
func sanitizeKey(key string) string {
	key = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, key)

	const maxLength = 250
	if len(key) > maxLength {
		key = key[:maxLength]
	}

	return key
}

type Clock interface {
	Now() time.Time
}

type RateLimiterConfig struct {
	Window              time.Duration
	MaxRequests         int
	Methods             []string
	XffClientIpPosition int
}

type RateLimiterConfigProvider interface {
	GetRateLimiterConfig() *RateLimiterConfig
}

type RateLimiter struct {
	config    *RateLimiterConfig
	store     MemoryStore
	clock     Clock
	knobs     knobs.Knobs
	configs   map[string]storeConfig
	configsMu sync.RWMutex
}

type RateLimiterOption func(*RateLimiter)

func WithClock(clock Clock) RateLimiterOption {
	return func(r *RateLimiter) {
		r.clock = clock
	}
}

func WithStore(store MemoryStore) RateLimiterOption {
	return func(r *RateLimiter) {
		r.store = store
	}
}

func WithKnobs(knobs knobs.Knobs) RateLimiterOption {
	return func(r *RateLimiter) {
		r.knobs = knobs
	}
}

type realClock struct{}

func (c *realClock) Now() time.Time {
	return time.Now()
}

type MemoryStore interface {
	Get(ctx context.Context, key string) (tokens uint64, remaining uint64, err error)
	Set(ctx context.Context, key string, tokens uint64, window time.Duration) error
	Take(ctx context.Context, key string) (tokens uint64, remaining uint64, reset uint64, ok bool, err error)
}

type realMemoryStore struct {
	// TODO: Update this to use the Redis store instead of the memory store.
	// See https://linear.app/lightsparkdev/issue/LIG-8247
	store limiter.Store
}

type storeConfig struct {
	tokens uint64
	window time.Duration
}

func (s *realMemoryStore) Get(ctx context.Context, key string) (tokens uint64, remaining uint64, err error) {
	return s.store.Get(ctx, key)
}

func (s *realMemoryStore) Set(ctx context.Context, key string, tokens uint64, window time.Duration) error {
	return s.store.Set(ctx, key, tokens, window)
}

func (s *realMemoryStore) Take(ctx context.Context, key string) (tokens uint64, remaining uint64, reset uint64, ok bool, err error) {
	return s.store.Take(ctx, key)
}

func NewRateLimiter(configOrProvider any, opts ...RateLimiterOption) (*RateLimiter, error) {
	var config *RateLimiterConfig
	switch v := configOrProvider.(type) {
	case *RateLimiterConfig:
		config = v
	case RateLimiterConfigProvider:
		config = v.GetRateLimiterConfig()
	default:
		return nil, fmt.Errorf("invalid config type: %T", configOrProvider)
	}

	rateLimiter := &RateLimiter{
		config:  config,
		clock:   &realClock{},
		knobs:   knobs.New(nil),
		configs: make(map[string]storeConfig),
	}

	for _, opt := range opts {
		opt(rateLimiter)
	}

	// Knob values should not be set to negative values—they will be cast to uint64.
	window := knobs.GetDurationSeconds(rateLimiter.knobs, knobs.KnobRateLimitPeriod, config.Window)
	maxRequests := uint64(rateLimiter.knobs.GetValue(knobs.KnobRateLimitLimit, float64(config.MaxRequests)))

	if rateLimiter.store == nil {
		defaultStore, err := memorystore.New(&memorystore.Config{
			Tokens:   maxRequests,
			Interval: window,
		})
		if err != nil {
			return nil, err
		}

		rateLimiter.store = &realMemoryStore{store: defaultStore}
	}

	return rateLimiter, nil
}

func (r *RateLimiter) getConfig(key string) (tokens uint64, window time.Duration, exists bool) {
	r.configsMu.RLock()
	defer r.configsMu.RUnlock()

	config, exists := r.configs[key]
	if !exists {
		return 0, 0, false
	}

	return config.tokens, config.window, true
}

func (r *RateLimiter) setConfig(key string, tokens uint64, window time.Duration) {
	r.configsMu.Lock()
	defer r.configsMu.Unlock()

	r.configs[key] = storeConfig{
		tokens: tokens,
		window: window,
	}
}

func (r *RateLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Check if the method is enabled.
		methodEnabled := r.knobs.RolloutRandomTarget(knobs.KnobGrpcServerMethodEnabled, &info.FullMethod, 100)
		if !methodEnabled {
			return nil, errors.UnavailableErrorf("The method is currently unavailable, please try again later.")
		}

		shouldLimit := slices.Contains(r.config.Methods, info.FullMethod)
		// A value of > 0 means to enforce rate limiting for the given method.
		// A value of 0 means to not enforce the limit for the given method.
		// Any other value means use the default configuration.
		methodLimitEnabled := int(r.knobs.GetValueTarget(knobs.KnobRateLimitMethods, &info.FullMethod, -1))
		if methodLimitEnabled > 0 {
			shouldLimit = true
		} else if methodLimitEnabled == 0 {
			shouldLimit = false
		}

		if !shouldLimit {
			return handler(ctx, req)
		}

		ip, err := GetClientIpFromHeader(ctx, r.config.XffClientIpPosition)
		if err != nil {
			return handler(ctx, req)
		}

		key := sanitizeKey(fmt.Sprintf("rl:%s:%s", info.FullMethod, ip))

		// Methods can also have specific rate limits and windows set for
		// them, so we need to check for that here. That should override the
		// default values, if they exist.
		methodMaxRequests := uint64(r.config.MaxRequests)
		rawMethodMaxRequests := int(r.knobs.GetValueTarget(knobs.KnobRateLimitLimit, &info.FullMethod, float64(r.config.MaxRequests)))

		if rawMethodMaxRequests == 0 {
			return handler(ctx, req)
		} else if rawMethodMaxRequests > 0 {
			methodMaxRequests = uint64(rawMethodMaxRequests)
		}

		methodWindow := knobs.GetTargetDurationSeconds(r.knobs, knobs.KnobRateLimitPeriod, &info.FullMethod, r.config.Window)

		// We only do the update if there's a change since it has the side
		// effect of reseting the bucket and thus the current rate limit status,
		// which we don't want to do in general.
		curMaxRequests, curWindow, exists := r.getConfig(key)
		hasChanged := !exists || curMaxRequests != methodMaxRequests || curWindow != methodWindow
		if hasChanged {
			// We ignore the error because, in the end, there's nothing we can
			// do about it. Additionally, in practice, the underlying real store
			// doesn't actually ever return an error.
			_ = r.store.Set(ctx, key, methodMaxRequests, methodWindow)
			r.setConfig(key, methodMaxRequests, methodWindow)
		}

		_, _, _, ok, err := r.store.Take(ctx, key)
		if err != nil {
			return nil, errors.InternalDependencyFailure(fmt.Errorf("rate limit error: %w", err))
		}
		if !ok {
			return nil, errors.ResourceExhaustedRateLimitExceeded(fmt.Errorf("rate limit exceeded"))
		}

		return handler(ctx, req)
	}
}
