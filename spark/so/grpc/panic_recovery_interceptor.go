package grpc

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/grpc"
)

var globalPanicCounter metric.Int64Counter

func init() {
	meter := otel.GetMeterProvider().Meter("spark.grpc")
	panicCounter, err := meter.Int64Counter(
		"rpc.server.panics_per_rpc",
		metric.WithDescription("Count of panics per RPC"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if panicCounter == nil {
			panicCounter = noop.Int64Counter{}
		}
	}

	globalPanicCounter = panicCounter
}

func PanicRecoveryInterceptor(returnDetailedPanicErrors bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		logger := logging.GetLoggerFromContext(ctx)

		// Wrap the entire handler in a recover block
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("Panic in handler",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
				)

				globalPanicCounter.Add(
					ctx,
					1,
					metric.WithAttributes(ParseFullMethod(info.FullMethod)...),
				)

				// Convert panic to error instead of re-panicking
				if returnDetailedPanicErrors {
					// Include details in testing/development
					panicMsg := fmt.Sprintf("%v", r)
					err = errors.InternalErrorf("Internal server error: %s", panicMsg)
				} else {
					// Generic message for production
					err = errors.InternalErrorf("Internal server error")
				}
				resp = nil
			}
		}()

		// Pass the request on down the chain
		return handler(ctx, req)
	}
}

func PanicRecoveryStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		logger := logging.GetLoggerFromContext(ss.Context())

		// Wrap the entire handler in a recover block
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("Panic in stream handler",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
				)

				globalPanicCounter.Add(
					ss.Context(),
					1,
					metric.WithAttributes(ParseFullMethod(info.FullMethod)...),
				)

				// Convert panic to error instead of re-panicking
				err = errors.InternalErrorf("Internal server error")
			}
		}()

		// Pass the request on down the chain
		return handler(srv, ss)
	}
}
