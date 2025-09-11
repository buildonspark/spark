package errors

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lightsparkdev/spark/so/grpcutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	grpcErrorsWithReasonTotal metric.Int64Counter
)

func init() {
	meter := otel.GetMeterProvider().Meter("spark.grpc")

	errWithReasonCounter, err := meter.Int64Counter(
		"rpc.server.error_with_reason",
		metric.WithDescription("Total number of gRPC requests that returned an error with a reason"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if errWithReasonCounter == nil {
			errWithReasonCounter = noop.Int64Counter{}
		}
	}
	grpcErrorsWithReasonTotal = errWithReasonCounter
}

func isInternalRPC(fullMethod string) bool {
	for _, prefix := range []string{
		"/spark_internal.SparkInternalService/",
		"/spark_ssp.SparkSspInternalService/",
		"/spark_token.SparkTokenInternalService/",
	} {
		if strings.HasPrefix(fullMethod, prefix) {
			return true
		}
	}
	return false
}

// ErrorStreamingInterceptor converts any error to a gRPC error to standardize downstream handling.
func ErrorStreamingInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, ss)
		return asGRPCError(err)
	}
}

// ErrorInterceptor combines wrapping, masking and metrics in a single interceptor.
func ErrorInterceptor(returnDetailedErrors bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}

		// Convert any error to a gRPC error to standardize downstream handling.
		err = asGRPCError(err)

		// Log full (pre-mask) code and reason.
		preCode, preReason := CodeAndReasonFrom(err)
		slog.Error("RPC failed", "method", info.FullMethod, "rpc_grpc_status_code", preCode.String(), "rpc_grpc_error_reason", preReason)

		// Emit metrics for the (pre-mask) error reason.
		st := status.Convert(err)
		codeStr := st.Code().String()
		attrs := append(grpcutil.ParseFullMethod(info.FullMethod), attribute.String("rpc_grpc_status_code", codeStr))
		_, reason := CodeAndReasonFrom(err)
		if reason == "" {
			reason = "NO_REASON"
		}
		attrs = append(attrs, attribute.String("rpc_grpc_error_reason", reason))
		grpcErrorsWithReasonTotal.Add(ctx, 1, metric.WithAttributes(attrs...))

		// Mask Internal/Unknown unless detailed errors are enabled or internal RPC.
		if st, ok := status.FromError(err); ok && (st.Code() == codes.Internal || st.Code() == codes.Unknown) {
			if !(returnDetailedErrors || isInternalRPC(info.FullMethod)) {
				err = status.Errorf(codes.Internal, "Something went wrong.")
			}
		}

		return resp, err
	}
}
