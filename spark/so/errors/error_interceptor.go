package errors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorInterceptor masks error messages for internal/unknown error codes
// to avoid leaking sensitive information.
func ErrorInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		resp, err = handler(ctx, req)
		if statusErr, ok := status.FromError(err); ok && (statusErr.Code() == codes.Internal || statusErr.Code() == codes.Unknown) {
			return resp, status.Errorf(codes.Internal, "Something went wrong.")
		}
		return resp, err
	}
}
