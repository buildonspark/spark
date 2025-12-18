package tokens

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	filterOutputIDs        = "output_ids"
	filterOwnerKeys        = "owner_keys"
	filterIssuerKeys       = "issuer_keys"
	filterTokenIdentifiers = "token_identifiers"
	filterTxHashes         = "tx_hashes"
	filterNone             = "none"
)

var meter = otel.Meter("handler.tokens")

var queryTokenTransactionsResultCount metric.Float64Histogram
var queryTokenTransactionsDuration metric.Float64Histogram

func init() {
	var err error

	queryTokenTransactionsResultCount, err = meter.Float64Histogram(
		"spark_token_query_transactions_result_count",
		metric.WithDescription("Distribution of result counts for QueryTokenTransactions"),
		metric.WithUnit("{count}"),
		metric.WithExplicitBucketBoundaries(generateResultCountBuckets(maxTokenTransactionPageSize)...),
	)
	if err != nil {
		panic(err)
	}

	queryTokenTransactionsDuration, err = meter.Float64Histogram(
		"spark_token_query_transactions_duration",
		metric.WithDescription("Duration of QueryTokenTransactions requests"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000),
	)
	if err != nil {
		panic(err)
	}
}

func generateResultCountBuckets(maxPageSize int) []float64 {
	buckets := []float64{0, 1}
	percentages := []float64{0.1, 0.25, 0.5, 0.75, 1.0}
	for _, pct := range percentages {
		bucket := float64(int(float64(maxPageSize) * pct))
		if bucket > buckets[len(buckets)-1] {
			buckets = append(buckets, bucket)
		}
	}
	return buckets
}

type queryMetricsRecorder struct {
	startTime    time.Time
	filters      string
	queryBackend string
}

func newQueryMetricsRecorder(params *queryParams, backend queryBackend) *queryMetricsRecorder {
	return &queryMetricsRecorder{
		startTime:    time.Now(),
		filters:      buildFiltersAttribute(params),
		queryBackend: string(backend),
	}
}

func (r *queryMetricsRecorder) record(ctx context.Context, resultCount int, err error) {
	duration := time.Since(r.startTime).Seconds() * 1000

	attrs := []attribute.KeyValue{
		attribute.String("filters", r.filters),
		attribute.String("query_backend", r.queryBackend),
		attribute.Bool("success", err == nil),
	}
	opts := metric.WithAttributes(attrs...)

	queryTokenTransactionsResultCount.Record(ctx, float64(resultCount), opts)
	queryTokenTransactionsDuration.Record(ctx, duration, opts)
}

func buildFiltersAttribute(params *queryParams) string {
	var filters []string

	if len(params.outputIDs) > 0 {
		filters = append(filters, filterOutputIDs)
	}
	if len(params.ownerPublicKeys) > 0 {
		filters = append(filters, filterOwnerKeys)
	}
	if len(params.issuerPublicKeys) > 0 {
		filters = append(filters, filterIssuerKeys)
	}
	if len(params.tokenIdentifiers) > 0 {
		filters = append(filters, filterTokenIdentifiers)
	}
	if len(params.tokenTransactionHashes) > 0 {
		filters = append(filters, filterTxHashes)
	}

	if len(filters) == 0 {
		return filterNone
	}

	sort.Strings(filters)
	return strings.Join(filters, ",")
}
