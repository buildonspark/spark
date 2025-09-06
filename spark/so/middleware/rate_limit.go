package middleware

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
	config *RateLimiterConfig
	store  MemoryStore
	clock  Clock
	knobs  knobs.Knobs
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
	Take(ctx context.Context, key string) (tokens uint64, remaining uint64, reset uint64, ok bool, err error)
}

type realMemoryStore struct {
	store limiter.Store
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
		config: config,
		clock:  &realClock{},
		knobs:  nil,
	}

	for _, opt := range opts {
		opt(rateLimiter)
	}

	interval := config.Window
	maxRequests := uint64(config.MaxRequests)
	// Knob values should not be set to negative values—they will be cast to uint64.
	if rateLimiter.knobs != nil {
		interval = time.Duration(uint64(rateLimiter.knobs.GetValue(knobs.KnobRateLimitPeriod, float64(config.Window)))) * time.Second
		maxRequests = uint64(rateLimiter.knobs.GetValue(knobs.KnobRateLimitLimit, float64(config.MaxRequests)))
	}

	if rateLimiter.store == nil {
		defaultStore, err := memorystore.New(&memorystore.Config{
			Tokens:   maxRequests,
			Interval: interval,
		})
		if err != nil {
			return nil, err
		}

		rateLimiter.store = &realMemoryStore{store: defaultStore}
	}

	return rateLimiter, nil
}

func (r *RateLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Check if the method is enabled.
		if r.knobs != nil {
			methodEnabled := r.knobs.RolloutRandomTarget(knobs.KnobGrpcServerMethodEnabled, &info.FullMethod, 100)
			if !methodEnabled {
				return nil, errors.UnavailableErrorf("The method is currently unavailable, please try again later.")
			}
		}

		shouldLimit := slices.Contains(r.config.Methods, info.FullMethod)
		if r.knobs != nil {
			// A value of > 0 means to enforce rate limiting for the given method.
			// A value of 0 means to not enforce the limit for the given method.
			// Any other value means use the default configuration.
			methodLimitEnabled := int(r.knobs.GetValueTarget(knobs.KnobRateLimitMethods, &info.FullMethod, -1))
			if methodLimitEnabled > 0 {
				shouldLimit = true
			} else if methodLimitEnabled == 0 {
				shouldLimit = false
			}
		}

		if !shouldLimit {
			return handler(ctx, req)
		}

		ip, err := GetClientIpFromHeader(ctx, r.config.XffClientIpPosition)
		if err != nil {
			return handler(ctx, req)
		}

		key := sanitizeKey(fmt.Sprintf("rl:%s:%s", info.FullMethod, ip))
		_, _, _, ok, err := r.store.Take(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("rate limit error: %w", err)
		}
		if !ok {
			return nil, errors.RateLimitExceededError()
		}

		return handler(ctx, req)
	}
}
