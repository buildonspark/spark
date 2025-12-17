package grpc

import (
	"context"
	"time"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/grpcutil"
	"github.com/lightsparkdev/spark/so/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

var tokenMethods = map[string]struct{}{
	"/spark_token.SparkTokenService/broadcast_transaction":                            {},
	"/spark_token.SparkTokenService/start_transaction":                                {},
	"/spark_token.SparkTokenService/commit_transaction":                               {},
	"/spark_token.SparkTokenInternalService/prepare_transaction":                      {},
	"/spark_token.SparkTokenInternalService/sign_token_transaction_from_coordination": {},
	"/spark_token.SparkTokenInternalService/exchange_revocation_secrets_shares":       {},
}

// SparkTokenMetricsInterceptor collects metrics for Spark token transactions with the transaction type dimension.
func SparkTokenMetricsInterceptor() grpc.UnaryServerInterceptor {
	meter := otel.Meter("spark_token_metrics")

	sparkTokenTxStartedTotal, _ := meter.Int64Counter(
		"spark_token_transaction_started_total",
		metric.WithDescription("Total number of Spark token transaction RPCs started"),
		metric.WithUnit("1"),
	)

	sparkTokenTxHandledTotal, _ := meter.Int64Counter(
		"spark_token_transaction_handled_total",
		metric.WithDescription("Total number of Spark token transaction RPCs completed"),
		metric.WithUnit("1"),
	)

	sparkTokenTxDuration, _ := meter.Float64Histogram(
		"spark_token_transaction_duration_milliseconds",
		metric.WithDescription("Duration of Spark token transaction RPCs"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(
			// Standard gRPC latency buckets in milliseconds
			1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000,
		),
	)

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !isTokenTransactionMethod(info.FullMethod) {
			return handler(ctx, req)
		}

		txType := extractTransactionType(req)

		attrs := getSparkTokenAttributes(info.FullMethod, txType)
		sparkTokenTxStartedTotal.Add(ctx, 1, metric.WithAttributes(attrs...))

		startTime := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(startTime).Seconds() * 1000
		attrs = append(attrs, attribute.String("grpc_code", status.Code(err).String()))

		sparkTokenTxHandledTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		sparkTokenTxDuration.Record(ctx, duration, metric.WithAttributes(attrs...))

		return resp, err
	}
}

// getSparkTokenAttributes returns the attributes for Spark token metrics
func getSparkTokenAttributes(fullMethod string, txType string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("token_transaction_type", txType),
	}

	attrs = append(attrs, grpcutil.ParseFullMethod(fullMethod)...)

	return attrs
}

// isTokenTransactionMethod checks if the gRPC method is related to token transactions
func isTokenTransactionMethod(method string) bool {
	_, exists := tokenMethods[method]
	return exists
}

type hasPartialTokenTransaction interface {
	GetPartialTokenTransaction() *tokenpb.PartialTokenTransaction
}

type hasLegacyPartialTokenTransaction interface {
	GetPartialTokenTransaction() *tokenpb.TokenTransaction
}

type hasFinalTokenTransaction interface {
	GetFinalTokenTransaction() *tokenpb.TokenTransaction
}

// extractTransactionType extracts the transaction type from the request
func extractTransactionType(req any) string {
	var tokenTx utils.InferrableTokenTransaction

	switch r := req.(type) {
	case hasPartialTokenTransaction:
		tokenTx = r.GetPartialTokenTransaction()
	case hasFinalTokenTransaction:
		tokenTx = r.GetFinalTokenTransaction()
	case hasLegacyPartialTokenTransaction:
		tokenTx = r.GetPartialTokenTransaction()
	}

	if tokenTx == nil {
		return "UNKNOWN"
	}

	txType, err := utils.InferTokenTransactionType(tokenTx)
	if err == nil {
		return txType.String()
	}

	return "UNKNOWN"
}
