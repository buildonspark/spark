package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ValidationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Validate the request proto if it implements Validate().
		if v, ok := req.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
			}
		}

		// Pass the request on down the chain.
		resp, err := handler(ctx, req)
		if err != nil {
			return nil, err
		}

		// Validate the response proto if it implements Validate().
		if v, ok := resp.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return nil, status.Errorf(codes.Internal, "invalid response: %v", err)
			}
		}

		return resp, nil
	}
}
