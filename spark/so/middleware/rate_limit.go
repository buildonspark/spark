package middleware

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/sethvargo/go-limiter"
	"github.com/sethvargo/go-limiter/memorystore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

type RateLimiterConfig struct {
	Window      time.Duration
	MaxRequests int
	Methods     []string
}

type RateLimiterConfigProvider interface {
	GetRateLimiterConfig() *RateLimiterConfig
}

type RateLimiter struct {
	config *RateLimiterConfig
	store  limiter.Store
}

func NewRateLimiter(configOrProvider interface{}) (*RateLimiter, error) {
	var config *RateLimiterConfig
	switch v := configOrProvider.(type) {
	case *RateLimiterConfig:
		config = v
	case RateLimiterConfigProvider:
		config = v.GetRateLimiterConfig()
	default:
		return nil, fmt.Errorf("invalid config type: %T", configOrProvider)
	}

	store, err := memorystore.New(&memorystore.Config{
		Tokens:   uint64(config.MaxRequests),
		Interval: config.Window,
	})
	if err != nil {
		return nil, err
	}

	return &RateLimiter{
		config: config,
		store:  store,
	}, nil
}

func (r *RateLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		shouldLimit := slices.Contains(r.config.Methods, info.FullMethod)

		if !shouldLimit {
			return handler(ctx, req)
		}

		ip := getClientIP(ctx)
		if ip == "" {
			return handler(ctx, req)
		}

		key := sanitizeKey(fmt.Sprintf("rl:%s:%s", info.FullMethod, ip))
		_, _, _, ok, err := r.store.Take(ctx, key)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "rate limit error: %v", err)
		}
		if !ok {
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}

		return handler(ctx, req)
	}
}

func getClientIP(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	if xff := md.Get("x-forwarded-for"); len(xff) > 0 {
		// Take the first IP in the list (the original client)
		ips := strings.Split(xff[0], ",")
		return strings.TrimSpace(ips[0])
	}

	if xri := md.Get("x-real-ip"); len(xri) > 0 {
		return xri[0]
	}

	return ""
}
