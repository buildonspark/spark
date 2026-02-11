package grpc

import (
	"context"
	"fmt"

	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/grpc"
)

// MethodDisableInterceptor returns an interceptor that blocks any gRPC method
// when the KnobGrpcServerMethodEnabled knob is disabled for that method.
//
// Usage: Set "spark.so.grpc.server.method.enabled@/service/Method" to 0
// in the knobs ConfigMap to disable that method on all operators.
func MethodDisableInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobGrpcServerMethodEnabled, &info.FullMethod, 100) <= 0 {
			return nil, sparkerrors.UnavailableMethodDisabled(fmt.Errorf("the method is currently unavailable, please try again later"))
		}
		return handler(ctx, req)
	}
}

// MethodDisableStreamInterceptor returns a stream interceptor that blocks any gRPC method
// when the KnobGrpcServerMethodEnabled knob is disabled for that method.
func MethodDisableStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if knobs.GetKnobsService(ss.Context()).GetValueTarget(knobs.KnobGrpcServerMethodEnabled, &info.FullMethod, 100) <= 0 {
			return sparkerrors.UnavailableMethodDisabled(fmt.Errorf("the method is currently unavailable, please try again later"))
		}
		return handler(srv, ss)
	}
}
