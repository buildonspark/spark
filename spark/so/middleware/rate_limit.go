package middleware

import (
	"context"
	"fmt"
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

/*
Rate limiter overview

What this middleware does
- Enforces per-client-IP rate limits for gRPC unary methods using a token-bucket per tier.
- Enforcement is always per-method. Only the explicit global scope aggregates across methods.
- There is no base/window config. Limits are applied only when a tier-suffixed knob is set.

Hardcoded tiers (no discovery)
- Supported suffixes: #1s, #1m, #10m, #1h, #24h

Configuration via knobs:
- Method: spark.so.ratelimit.limit@/pkg.Service/Method#1s = <max_requests>
- Service method-name prefix (longest-match on method name):
  spark.so.ratelimit.limit@/pkg.Service/^start#1s = <max_requests>
- Service: spark.so.ratelimit.limit@/pkg.Service/#1s = <max_requests>
- Global: spark.so.ratelimit.limit@global#1s = <max_requests>

Notes on precedence and behavior
- For each tier, we compute:
  - Per-method limit from the first configured (>= 0) among: Method (exact FullMethod), Service/^<method-name-prefix> (longest prefix).
  - Service limit directly from Service/.
  - Global limit directly from Global.
- We enforce all configured scopes for the tier: per-method (if > 0), service (if > 0), and global (if > 0).
- If none are configured for a tier, that tier is bypassed.

Enforcement in-memory keys (per-client-IP)
- Per-method scope key: rl:/pkg.Service/Method#<tier>:<ip>
- Service scope key: rl:/pkg.Service/#<tier>:<ip>
- Global scope key: rl:global#<tier>:<ip>

Other knobs
- Exclude an IP entirely: spark.so.ratelimit.exclude_ips@<ip> = 1
- Kill switch for a method (independent of rate limiting): spark.so.grpc.server.method.enabled@/pkg.Service/Method = 0.
*/

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
	tiers     []tier
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

type tier struct {
	suffix string
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

	rateLimiter.tiers = []tier{
		{suffix: "#1s", window: time.Second},
		{suffix: "#1m", window: time.Minute},
		{suffix: "#10m", window: 10 * time.Minute},
		{suffix: "#1h", window: time.Hour},
		{suffix: "#24h", window: 24 * time.Hour},
	}

	if rateLimiter.store == nil {
		// Use default dummy configuration for initialization.
		// Configured rate limits will always override these values via Set.
		defaultStore, err := memorystore.New(&memorystore.Config{
			Tokens:   1,
			Interval: time.Second,
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

// takeToken enforces a single bucket identified by tierScope and ip.
// It ensures the store's bucket config matches the desired tokens/window
// and attempts to take a token, returning an appropriate error on failure.
func (r *RateLimiter) takeToken(ctx context.Context, tierScope string, ip string, tokens uint64, window time.Duration, label string) error {
	tierKey := sanitizeKey(fmt.Sprintf("rl:%s:%s", tierScope, ip))

	curTokens, curWindow, exists := r.getConfig(tierKey)
	hasChanged := !exists || curTokens != tokens || curWindow != window
	if hasChanged {
		_ = r.store.Set(ctx, tierKey, tokens, window)
		r.setConfig(tierKey, tokens, window)
	}

	_, _, _, ok, err := r.store.Take(ctx, tierKey)
	if err != nil {
		return errors.UnavailableDataStore(fmt.Errorf("%s rate limit error: %w", label, err))
	}
	if !ok {
		return errors.ResourceExhaustedRateLimitExceeded(fmt.Errorf("%s rate limit exceeded", label))
	}
	return nil
}

func (r *RateLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Check if the method is enabled.
		methodEnabled := r.knobs.RolloutRandomTarget(knobs.KnobGrpcServerMethodEnabled, &info.FullMethod, 100)
		if !methodEnabled {
			return nil, errors.UnimplementedMethodDisabled(fmt.Errorf("the method is currently unavailable, please try again later"))
		}

		ip, err := GetClientIpFromHeader(ctx, r.config.XffClientIpPosition)
		if err != nil {
			return handler(ctx, req)
		}
		// Check for excluded IPs. A value of > 0 means to exclude the IP from rate limiting.
		isIpExcluded := r.knobs.GetValueTarget(knobs.KnobRateLimitExcludeIps, &ip, 0)
		if isIpExcluded > 0 {
			return handler(ctx, req)
		}

		for _, t := range r.tiers {
			suffix := t.suffix
			if suffix == "" {
				continue
			}
			methodTarget := info.FullMethod + suffix
			serviceEnd := strings.LastIndex(info.FullMethod, "/")
			servicePath := info.FullMethod
			methodName := ""
			if serviceEnd >= 0 {
				servicePath = info.FullMethod[:serviceEnd+1] // includes trailing '/'
				methodName = info.FullMethod[serviceEnd+1:]
			}
			serviceTarget := servicePath + suffix // e.g. /pkg.Service/#1s
			globalTarget := "global" + suffix     // eg. global#1s
			// Method-name prefix anchor, longest match: /pkg.Service/^prefix#1s
			prefixTierLimit := -1
			if len(methodName) > 0 {
				for i := len(methodName); i >= 1; i-- {
					candidateKey := servicePath + "^" + methodName[:i] + suffix
					v := int(r.knobs.GetValueTarget(knobs.KnobRateLimitLimit, &candidateKey, -1))
					if v >= 0 {
						prefixTierLimit = v
						break
					}
				}
			}
			methodTierLimit := int(r.knobs.GetValueTarget(knobs.KnobRateLimitLimit, &methodTarget, -1))
			serviceTierLimit := int(r.knobs.GetValueTarget(knobs.KnobRateLimitLimit, &serviceTarget, -1))
			globalTierLimit := int(r.knobs.GetValueTarget(knobs.KnobRateLimitLimit, &globalTarget, -1))

			// Resolve per-method candidate via precedence (method > prefix)
			methodCandidate := -1
			if methodTierLimit >= 0 {
				methodCandidate = methodTierLimit
			} else if prefixTierLimit >= 0 {
				methodCandidate = prefixTierLimit
			}
			tierWindow := t.window

			if methodCandidate > 0 {
				if err := r.takeToken(ctx, info.FullMethod+suffix, ip, uint64(methodCandidate), tierWindow, "per-method"); err != nil {
					return nil, err
				}
			}

			if serviceTierLimit > 0 {
				if err := r.takeToken(ctx, servicePath+suffix, ip, uint64(serviceTierLimit), tierWindow, "service"); err != nil {
					return nil, err
				}
			}

			if globalTierLimit > 0 {
				if err := r.takeToken(ctx, "global"+suffix, ip, uint64(globalTierLimit), tierWindow, "global"); err != nil {
					return nil, err
				}
			}
		}

		return handler(ctx, req)
	}
}
