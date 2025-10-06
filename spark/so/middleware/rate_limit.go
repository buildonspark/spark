package middleware

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/sethvargo/go-limiter"
	"github.com/sethvargo/go-limiter/memorystore"
	"google.golang.org/grpc"
)

/*
Rate limiter overview

What this middleware does
- Enforces rate limits on gRPC unary methods. If this rate limit is exceeded return early with a ResourceExhaustedError.
- Supports rate limits at the method, service, or global levelLimits are applied at the method, service, and global level.
- Supports rate limits for the following windows / tiers: #1s, #1m, #10m, #1h, #24h
- Supports rate limits over different dimesions: IPs or client public keys

Specific configurations via knobs:
- Method: spark.so.ratelimit.limit@/pkg.Service/Method#1s = <max_requests>
- Method (dimension-specific): spark.so.ratelimit.limit@/pkg.Service/Method:ip#1s or :pubkey#1s
- Service method-name prefix (longest-match on method name):
  spark.so.ratelimit.limit@/pkg.Service/^start#1s = <max_requests>
  spark.so.ratelimit.limit@/pkg.Service/^start:ip#1s or :pubkey#1s
- Service: spark.so.ratelimit.limit@/pkg.Service/#1s = <max_requests>
  spark.so.ratelimit.limit@/pkg.Service/:ip#1s or :pubkey#1s
- Global: spark.so.ratelimit.limit@global#1s = <max_requests>
  spark.so.ratelimit.limit@global:ip#1s or :pubkey#1s

Notes on precedence and behavior
- For each tier and dimension, we compute:
  - For per-method scope limits, Method (exact FullMethod >= 0), takes precedence over prefix scopes. If multiple prefix scopes, the longest prefix is used.
  - For per-dimension limits, :ip, :pubkey (>= 0) takes precedence over limits without a dimension selector.
- We enforce all configured scopes for each tier: per-method (if > 0), service (if > 0), and global (if > 0).
- If none are configured for a tier, that tier is bypassed.

Dimension selector behavior
- Per-dimension limits are optional. Limits without a dimension selector apply to both (ip and pubkey) by default.
- Providing both :ip and :pubkey allows different limits per dimension.
- If a selector is provided for a dimension, the base value is ignored for that dimension.


Enforcement in-memory keys (per-dimension)
- Per-method scope key: rl:/<service-name>/<method-name>#<tier>:<dimension>
- Service scope key: rl:/<service-name>/#<tier>:<dimension>
- Global scope key: rl:global#<tier>:<dimension>

Other knobs
- Exclude an IP from rate limiting: spark.so.ratelimit.exclude_ips@<ip> = 1
- Exclude a pubkey from rate limiting: spark.so.ratelimit.exclude_pubkeys@<hex_pubkey> = 1
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

// takeToken enforces a single dimension identified by tierScope and ip/pubkey.
// It ensures the store's dimension config matches the desired tokens/window
// and attempts to take a token, returning an appropriate error on failure.
func (r *RateLimiter) takeToken(ctx context.Context, tierScope string, dimension string, tokens uint64, window time.Duration, label string) error {
	tierKey := sanitizeKey(fmt.Sprintf("rl:%s:%s", tierScope, dimension))

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

func (r *RateLimiter) getLimitForKey(key string) int {
	return int(r.knobs.GetValueTarget(knobs.KnobRateLimitLimit, &key, -1))
}

func (r *RateLimiter) resolveMethodLimits(servicePath, methodName, fullMethod, suffix string) (ipLimit int, pubkeyLimit int) {
	methodBase := r.getLimitForKey(fullMethod + suffix)
	methodIp := r.getLimitForKey(fullMethod + ":ip" + suffix)
	methodPub := r.getLimitForKey(fullMethod + ":pubkey" + suffix)

	prefixBase, prefixIp, prefixPub := -1, -1, -1
	if methodName != "" {
		for i := len(methodName); i >= 1; i-- {
			prefix := servicePath + "^" + methodName[:i]
			if prefixIp < 0 {
				if v := r.getLimitForKey(prefix + ":ip" + suffix); v >= 0 {
					prefixIp = v
				}
			}
			if prefixPub < 0 {
				if v := r.getLimitForKey(prefix + ":pubkey" + suffix); v >= 0 {
					prefixPub = v
				}
			}
			if prefixBase < 0 {
				if v := r.getLimitForKey(prefix + suffix); v >= 0 {
					prefixBase = v
				}
			}
			if prefixIp >= 0 && prefixPub >= 0 && prefixBase >= 0 {
				break
			}
		}
	}

	resolvedIp := -1
	switch {
	case methodIp >= 0:
		resolvedIp = methodIp
	case methodBase >= 0:
		resolvedIp = methodBase
	case prefixIp >= 0:
		resolvedIp = prefixIp
	case prefixBase >= 0:
		resolvedIp = prefixBase
	}

	resolvedPub := -1
	switch {
	case methodPub >= 0:
		resolvedPub = methodPub
	case methodBase >= 0:
		resolvedPub = methodBase
	case prefixPub >= 0:
		resolvedPub = prefixPub
	case prefixBase >= 0:
		resolvedPub = prefixBase
	}

	return resolvedIp, resolvedPub
}

func (r *RateLimiter) resolveScopeLimits(baseKey string, suffix string) (ipLimit int, pubkeyLimit int) {
	base := r.getLimitForKey(baseKey + suffix)
	ip := r.getLimitForKey(baseKey + ":ip" + suffix)
	pub := r.getLimitForKey(baseKey + ":pubkey" + suffix)

	resolvedIp := -1
	if ip >= 0 {
		resolvedIp = ip
	} else if base >= 0 {
		resolvedIp = base
	}

	resolvedPub := -1
	if pub >= 0 {
		resolvedPub = pub
	} else if base >= 0 {
		resolvedPub = base
	}

	return resolvedIp, resolvedPub
}

func (r *RateLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Check if the method is enabled.
		methodEnabled := r.knobs.RolloutRandomTarget(knobs.KnobGrpcServerMethodEnabled, &info.FullMethod, 100)
		if !methodEnabled {
			return nil, errors.UnimplementedMethodDisabled(fmt.Errorf("the method is currently unavailable, please try again later"))
		}

		// Build potential dimensions based on availability (dimension selection is driven by knob selectors)
		var pubkeyBucket, ipBucket string
		havePubkey, haveIP := false, false
		var identityHex string
		var clientIP string

		if session, err := authn.GetSessionFromContext(ctx); err == nil && session != nil {
			identityHex = session.IdentityPublicKey().ToHex()
		}

		if v, err := GetClientIpFromHeader(ctx, r.config.XffClientIpPosition); err == nil && v != "" {
			clientIP = v
		}

		// If either IP or pubkey is excluded, bypass all rate limiting entirely.
		if identityHex != "" {
			if r.knobs.GetValueTarget(knobs.KnobRateLimitExcludePubkeys, &identityHex, 0) > 0 {
				return handler(ctx, req)
			}
			pubkeyBucket = "pubkey:" + identityHex
			havePubkey = true
		}
		if clientIP != "" {
			if r.knobs.GetValueTarget(knobs.KnobRateLimitExcludeIps, &clientIP, 0) > 0 {
				return handler(ctx, req)
			}
			ipBucket = "ip:" + clientIP
			haveIP = true
		}

		if !havePubkey && !haveIP {
			// No usable dimension; bypass rate limiting.
			return handler(ctx, req)
		}

		for _, t := range r.tiers {
			suffix := t.suffix
			if suffix == "" {
				continue
			}
			serviceEnd := strings.LastIndex(info.FullMethod, "/")
			servicePath := info.FullMethod
			methodName := ""
			if serviceEnd >= 0 {
				servicePath = info.FullMethod[:serviceEnd+1] // includes trailing '/'
				methodName = info.FullMethod[serviceEnd+1:]
			}
			// Resolve per-scope, per-dimension limits with precedence
			methodIpLimit, methodPubkeyLimit := r.resolveMethodLimits(servicePath, methodName, info.FullMethod, suffix)
			serviceIpLimit, servicePubkeyLimit := r.resolveScopeLimits(servicePath, suffix)
			globalIpLimit, globalPubkeyLimit := r.resolveScopeLimits("global", suffix)
			tierWindow := t.window

			if havePubkey && methodPubkeyLimit > 0 {
				if err := r.takeToken(ctx, info.FullMethod+suffix, pubkeyBucket, uint64(methodPubkeyLimit), tierWindow, "per-method"); err != nil {
					return nil, err
				}
			}
			if haveIP && methodIpLimit > 0 {
				if err := r.takeToken(ctx, info.FullMethod+suffix, ipBucket, uint64(methodIpLimit), tierWindow, "per-method"); err != nil {
					return nil, err
				}
			}

			if havePubkey && servicePubkeyLimit > 0 {
				if err := r.takeToken(ctx, servicePath+suffix, pubkeyBucket, uint64(servicePubkeyLimit), tierWindow, "service"); err != nil {
					return nil, err
				}
			}
			if haveIP && serviceIpLimit > 0 {
				if err := r.takeToken(ctx, servicePath+suffix, ipBucket, uint64(serviceIpLimit), tierWindow, "service"); err != nil {
					return nil, err
				}
			}

			if havePubkey && globalPubkeyLimit > 0 {
				if err := r.takeToken(ctx, "global"+suffix, pubkeyBucket, uint64(globalPubkeyLimit), tierWindow, "global"); err != nil {
					return nil, err
				}
			}
			if haveIP && globalIpLimit > 0 {
				if err := r.takeToken(ctx, "global"+suffix, ipBucket, uint64(globalIpLimit), tierWindow, "global"); err != nil {
					return nil, err
				}
			}
		}

		return handler(ctx, req)
	}
}
