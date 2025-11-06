package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// DateHeaderInterceptor adds a date header
func DateHeaderInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)

		if err == nil {
			dateHeader := time.Now().UTC().Format(time.RFC1123)
			md := metadata.Pairs("date", dateHeader)
			if headerErr := grpc.SetHeader(ctx, md); headerErr != nil {
				return resp, headerErr
			}
		}

		return resp, err
	}
}
