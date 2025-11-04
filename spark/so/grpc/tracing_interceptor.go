package grpc

import (
	"context"

	"google.golang.org/grpc"
)

func TracingInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, span := tracer.Start(ctx, "TracingInterceptor")
		defer span.End()

		return handler(ctx, req)
	}
}
