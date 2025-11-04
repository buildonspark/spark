package middleware

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/grpcutil"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/sethvargo/go-limiter"
	"github.com/sethvargo/go-limiter/memorystore"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	config *RateLimiterConfig
	store  MemoryStore
	clock  Clock
	knobs  knobs.Knobs
	tiers  []tier

	// Metrics fields
	utilizationHistogram metric.Float64Histogram
	breachCounter        metric.Int64Counter
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

type tier struct {
	suffix string
	window time.Duration
}

// rateLimitEnforcementParams encapsulates inputs for a single enforcement observation
type rateLimitEnforcementParams struct {
	// Scope indicates the scope being enforced: "method", "service", or "global".
	// This controls how the key is constructed in enforceAndObserve and how metrics are attributed.
	Scope string

	// TierSuffix is the canonical window suffix (e.g., "#1s", "#1m", "#10m", "#1h", "#24h").
	// It’s appended to the scope key and should correspond to the Window duration.
	TierSuffix string

	// Dimension selects which identity dimension to enforce: "ip" or "pubkey".
	// This is used for metrics and to compose the bucket identity.
	Dimension string

	// Bucket is the identity value for the chosen dimension (no prefix),
	// e.g., "203.0.113.1" for ip or "<hex>" for pubkey. If empty, enforcement is skipped.
	Bucket string

	// Limit is the maximum number of tokens allowed within Window for this scope/dimension.
	// A value <= 0 disables enforcement for this observation.
	Limit int

	// FullMethod is the gRPC full method path (e.g., "/pkg.Service/Method").
	// Used to build method/service scoped keys and for metrics attribution.
	FullMethod string

	// ServicePath is the gRPC service path including trailing slash (e.g., "/pkg.Service/").
	// Precomputed to avoid repeated parsing.
	ServicePath string
}

// serviceKeyFromPath normalizes a service path like "/pkg.Service/" to "pkg.Service"
func serviceKeyFromPath(servicePath string) string {
	return strings.TrimSuffix(strings.TrimPrefix(servicePath, "/"), "/")
}

// rateLimitKey returns the grouping key used for metrics for a given scope
func rateLimitKey(scope string, fullMethod string, servicePath string) string {
	switch scope {
	case "method":
		return fullMethod
	case "service":
		return serviceKeyFromPath(servicePath)
	case "global":
		return "global"
	default:
		return ""
	}
}

// metricAttributes constructs standard attributes for rate limit metrics
func metricAttributes(scope string, tierSuffix string, dimension string, limitKey string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("scope", scope),
		attribute.String("tier", tierSuffix),
		attribute.String("dimension", dimension),
		attribute.String("key", limitKey),
	}
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
		config: config,
		clock:  &realClock{},
		knobs:  knobs.New(nil),
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

	meter := otel.GetMeterProvider().Meter("spark.grpc")
	rateLimiter.utilizationHistogram = newHistogramWithFallback(
		meter,
		"rpc.server.ratelimit_utilization",
		metric.WithDescription("Token bucket utilization at request time (0.0-1.0)"),
		metric.WithUnit("1"),
		metric.WithExplicitBucketBoundaries(0.0, 0.1, 0.25, 0.5, 0.75, 0.9, 1.0),
	)
	rateLimiter.breachCounter = newCounterWithFallback(
		meter,
		"rpc.server.ratelimit_exceeded_total",
		metric.WithDescription("Total number of requests rejected by rate limiting"),
		metric.WithUnit("1"),
	)

	return rateLimiter, nil
}

// windowForSuffix returns the configured time window for a given tier suffix.
func (r *RateLimiter) windowForSuffix(s string) time.Duration {
	for _, t := range r.tiers {
		if t.suffix == s {
			return t.window
		}
	}
	return 0
}

// takeTokenForKey enforces a single fully-qualified bucket key.
// It ensures the store's bucket config matches the desired tokens/window
// and attempts to take a token, returning an appropriate error on failure.
func (r *RateLimiter) takeTokenForKey(ctx context.Context, key string, tokens uint64, window time.Duration, label string) (uint64, uint64, error) {
	currentCapacity, remaining, err := r.store.Get(ctx, key)
	if err != nil {
		// If we can't even Get the state of the bucket, we must fail open.
		logger := logging.GetLoggerFromContext(ctx)
		logger.Error(fmt.Sprintf("Rate limit store failed on Get, failing open. key=%s, err=%v", sanitizeKey(key), err))
		return tokens, tokens, nil
	}
	if currentCapacity != tokens {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Info(fmt.Sprintf(
			"Rate limit bucket capacity mismatch, resetting. key=%s, current=%d, expected=%d",
			sanitizeKey(key), currentCapacity, tokens,
		))
		if err := r.store.Set(ctx, key, tokens, window); err != nil {
			logger.Error(fmt.Sprintf("Failed to set rate limit bucket, failing open. key=%s, err=%v", key, err))
			return tokens, tokens, nil
		}
		// After a reset, assume the bucket is now full.
		remaining = tokens
	}

	if remaining == 0 {
		return tokens, 0, errors.ResourceExhaustedRateLimitExceeded(fmt.Errorf("%s rate limit exceeded", label))
	}

	// We believe we have tokens, so now we attempt to take one.
	_, _, _, ok, err := r.store.Take(ctx, key)
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Error(fmt.Sprintf(
			"Rate limit store failed on Take, failing open. key=%s, err=%v",
			sanitizeKey(key), err,
		))
		return tokens, tokens, nil
	}

	if !ok {
		// This indicates a race condition where another request took the last token between our Get and Take.
		// Don't rate limit this request to be maximally cautious in case of unexpected system behavior.
		logger := logging.GetLoggerFromContext(ctx)
		logger.Warn(fmt.Sprintf(
			"Rate limit race condition: Get reported tokens, but Take failed. Allowing request. key=%s",
			sanitizeKey(key),
		))
		return tokens, tokens, nil
	}

	// Success. The Take operation decremented the token count.
	return tokens, remaining - 1, nil
}

func (r *RateLimiter) observeUtilization(ctx context.Context, p rateLimitEnforcementParams, capacity uint64, remaining uint64) {
	if capacity == 0 {
		return
	}

	limitKey := rateLimitKey(p.Scope, p.FullMethod, p.ServicePath)
	attrs := metricAttributes(p.Scope, p.TierSuffix, p.Dimension, limitKey)
	attrs = append(attrs, grpcutil.ParseFullMethod(p.FullMethod)...)
	utilizationPercentage := math.Max(0, math.Min(float64(capacity-remaining)/float64(capacity), 1))
	r.recordUtilizationMetric(ctx, utilizationPercentage, attrs)
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
		ctx, span := tracer.Start(ctx, "RateLimiterInterceptor")
		defer span.End()

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
			pubkeyBucket = identityHex
			havePubkey = true
		}
		if clientIP != "" {
			if r.knobs.GetValueTarget(knobs.KnobRateLimitExcludeIps, &clientIP, 0) > 0 {
				return handler(ctx, req)
			}
			ipBucket = clientIP
			haveIP = true
		}

		if !havePubkey && !haveIP {
			// No usable dimension; bypass rate limiting.
			return handler(ctx, req)
		}

		service, method := grpcutil.ParseFullMethodStrings(info.FullMethod)
		servicePath := "/" + service + "/" // includes trailing '/'
		methodName := method

		// Build list of available dimensions
		dimensions := make([]struct {
			name   string
			bucket string
		}, 0, 2)
		if havePubkey {
			dimensions = append(dimensions, struct {
				name   string
				bucket string
			}{name: "pubkey", bucket: pubkeyBucket})
		}
		if haveIP {
			dimensions = append(dimensions, struct {
				name   string
				bucket string
			}{name: "ip", bucket: ipBucket})
		}

		for _, t := range r.tiers {
			suffix := t.suffix
			if suffix == "" {
				continue
			}

			// Resolve per-scope limits once per tier
			methodIpLimit, methodPubkeyLimit := r.resolveMethodLimits(servicePath, methodName, info.FullMethod, suffix)
			serviceIpLimit, servicePubkeyLimit := r.resolveScopeLimits(servicePath, suffix)
			globalIpLimit, globalPubkeyLimit := r.resolveScopeLimits("global", suffix)

			// Helper to DRY enforcement and utilization recording across method/service/global
			enforceAcrossScopes := func(base rateLimitEnforcementParams, methodLimit int, serviceLimit int, globalLimit int) error {
				base.Scope = "method"
				base.Limit = methodLimit
				if err := r.enforceAndObserve(ctx, base); err != nil {
					return err
				}
				base.Scope = "service"
				base.Limit = serviceLimit
				if err := r.enforceAndObserve(ctx, base); err != nil {
					return err
				}
				base.Scope = "global"
				base.Limit = globalLimit
				if err := r.enforceAndObserve(ctx, base); err != nil {
					return err
				}
				return nil
			}

			// Base parameters for this tier and method/service
			baseTierParams := rateLimitEnforcementParams{
				TierSuffix:  suffix,
				FullMethod:  info.FullMethod,
				ServicePath: servicePath,
			}

			for _, d := range dimensions {
				p := baseTierParams
				p.Dimension = d.name
				p.Bucket = d.bucket
				var methodLimit, serviceLimit, globalLimit int
				if d.name == "ip" {
					methodLimit, serviceLimit, globalLimit = methodIpLimit, serviceIpLimit, globalIpLimit
				} else { // pubkey
					methodLimit, serviceLimit, globalLimit = methodPubkeyLimit, servicePubkeyLimit, globalPubkeyLimit
				}
				if err := enforceAcrossScopes(p, methodLimit, serviceLimit, globalLimit); err != nil {
					return nil, err
				}
			}
		}

		return handler(ctx, req)
	}
}

// enforceAndObserve enforces a rate limit for a given scope/dimension and records utilization.
// Returns error if the rate limit store errors or if the limit is exceeded.
func (r *RateLimiter) enforceAndObserve(ctx context.Context, p rateLimitEnforcementParams) error {
	if p.Limit <= 0 || p.Bucket == "" {
		return nil
	}

	var tierScope string
	switch p.Scope {
	case "method":
		tierScope = p.FullMethod + p.TierSuffix
	case "service":
		tierScope = p.ServicePath + p.TierSuffix
	case "global":
		tierScope = "global" + p.TierSuffix
	default:
		tierScope = p.FullMethod + p.TierSuffix
	}

	window := r.windowForSuffix(p.TierSuffix)
	tierKey := sanitizeKey(fmt.Sprintf("rl:%s:%s:%s", tierScope, p.Dimension, p.Bucket))
	bucketCapacity, rem, err := r.takeTokenForKey(ctx, tierKey, uint64(p.Limit), window, p.Scope)
	if err != nil {
		st, _ := status.FromError(err)
		if st != nil && st.Code() == codes.ResourceExhausted {
			limitKey := rateLimitKey(p.Scope, p.FullMethod, p.ServicePath)
			attrs := metricAttributes(p.Scope, p.TierSuffix, p.Dimension, limitKey)
			attrs = append(attrs, grpcutil.ParseFullMethod(p.FullMethod)...)
			r.incrementBreachMetric(ctx, attrs)
			// Log breach with bucket identity
			logger := logging.GetLoggerFromContext(ctx)
			if logger != nil {
				logger.Warn(fmt.Sprintf(
					"rate limit exceeded: scope=%s tier=%s dimension=%s bucket=%s",
					p.Scope, p.TierSuffix, p.Dimension, p.Bucket,
				))
			}
		}
		return err
	}
	r.observeUtilization(ctx, p, bucketCapacity, rem)
	return nil
}

// recordUtilizationMetric emits the utilization histogram using the current MeterProvider.
func (r *RateLimiter) recordUtilizationMetric(ctx context.Context, utilizationPercentage float64, attrs []attribute.KeyValue) {
	histogram := r.utilizationHistogram
	histogram.Record(ctx, utilizationPercentage, metric.WithAttributes(attrs...))
}

// incrementBreachMetric increments the rate limit breach counter using the current MeterProvider.
func (r *RateLimiter) incrementBreachMetric(ctx context.Context, attrs []attribute.KeyValue) {
	counter := r.breachCounter
	counter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// newHistogramWithFallback tries to create a real histogram and falls back to a noop histogram on error.
func newHistogramWithFallback(m metric.Meter, name string, opts ...metric.Float64HistogramOption) metric.Float64Histogram {
	h, err := m.Float64Histogram(name, opts...)
	if err == nil {
		return h
	}
	otel.Handle(err)
	return noop.Float64Histogram{}
}

// newCounterWithFallback tries to create a real counter and falls back to a noop counter on error.
func newCounterWithFallback(m metric.Meter, name string, opts ...metric.Int64CounterOption) metric.Int64Counter {
	c, err := m.Int64Counter(name, opts...)
	if err == nil {
		return c
	}
	otel.Handle(err)
	return noop.Int64Counter{}
}
