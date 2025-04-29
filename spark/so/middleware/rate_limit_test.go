package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestRateLimiter(t *testing.T) {
	config := &RateLimiterConfig{
		Window:      time.Second,
		MaxRequests: 2,
		Methods:     []string{"/test.Service/TestMethod"},
	}

	t.Run("basic rate limiting", func(t *testing.T) {
		rateLimiter, err := NewRateLimiter(config)
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ interface{}) (interface{}, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctx, "request", info, handler)
		require.Error(t, err)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		assert.Equal(t, "rate limit exceeded", status.Convert(err).Message())
	})

	t.Run("method not rate limited", func(t *testing.T) {
		rateLimiter, err := NewRateLimiter(config)
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ interface{}) (interface{}, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/NotLimited"}

		for i := 0; i < 5; i++ {
			resp, err := interceptor(context.Background(), "request", info, handler)
			require.NoError(t, err)
			assert.Equal(t, "ok", resp)
		}
	})

	t.Run("window expiration", func(t *testing.T) {
		rateLimiter, err := NewRateLimiter(config)
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ interface{}) (interface{}, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))

		resp, err := interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		_, err = interceptor(ctx, "request", info, handler)
		require.Error(t, err)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		assert.Equal(t, "rate limit exceeded", status.Convert(err).Message())

		time.Sleep(2 * time.Second)

		resp, err = interceptor(ctx, "request", info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)
	})

	t.Run("different clients", func(t *testing.T) {
		rateLimiter, err := NewRateLimiter(config)
		require.NoError(t, err)

		interceptor := rateLimiter.UnaryServerInterceptor()
		handler := func(_ context.Context, _ interface{}) (interface{}, error) {
			return "ok", nil
		}
		info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/TestMethod"}

		ctx1 := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
			"x-forwarded-for": "1.2.3.4",
		}))
		ctx2 := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
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
		require.Error(t, err)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		assert.Equal(t, "rate limit exceeded", status.Convert(err).Message())

		_, err = interceptor(ctx2, "request", info, handler)
		require.Error(t, err)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		assert.Equal(t, "rate limit exceeded", status.Convert(err).Message())
	})
}
