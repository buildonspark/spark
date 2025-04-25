package grpc

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/lightsparkdev/spark/so/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func PanicRecoveryInterceptor(returnDetailedPanicErrors bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		logger := logging.GetLoggerFromContext(ctx)

		// Wrap the entire handler in a recover block
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("Panic in handler",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
					"request", req,
					"server_method", info.FullMethod,
				)

				// Convert panic to error instead of re-panicking
				if returnDetailedPanicErrors {
					// Include details in testing/development
					panicMsg := fmt.Sprintf("%v", r)
					err = status.Errorf(codes.Internal, "Internal server error: %s", panicMsg)
				} else {
					// Generic message for production
					err = status.Error(codes.Internal, "Internal server error")
				}
				resp = nil
			}
		}()

		// Pass the request on down the chain
		return handler(ctx, req)
	}
}
