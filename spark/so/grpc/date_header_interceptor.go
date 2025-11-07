package grpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TimestampHeaderInterceptor adds a date header and processing time header
func TimestampHeaderInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		startTime := time.Now()
		resp, err := handler(ctx, req)
		processingTimeMs := time.Since(startTime).Milliseconds()

		if err == nil {
			dateHeader := time.Now().UTC().Format(time.RFC1123)
			md := metadata.Pairs(
				"date", dateHeader,
				"x-processing-time-ms", fmt.Sprintf("%d", processingTimeMs),
			)
			if headerErr := grpc.SetHeader(ctx, md); headerErr != nil {
				return resp, headerErr
			}
		}

		return resp, err
	}
}
