package grpc

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error represents an error that can be converted to a gRPC error
type Error interface {
	error
	ToGRPCError() error
}

// wrapWithGRPCError wraps a response and an error into a gRPC error
func wrapWithGRPCError[T any](resp T, err error) (T, error) {
	if err != nil {
		return resp, toGRPCError(err)
	}
	return resp, nil
}

// toGRPCError converts any error to an appropriate gRPC error
func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	if grpcErr, ok := err.(Error); ok {
		return grpcErr.ToGRPCError()
	}

	// Default to Internal error
	return status.Error(codes.Internal, err.Error())
}
