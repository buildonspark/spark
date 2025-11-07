package ent

import (
	"context"
	"reflect"
	"strings"
	"time"

	"entgo.io/ent"
	"github.com/lightsparkdev/spark/common/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var (
	entReadCounter   metric.Int64Counter
	entInsertCounter metric.Int64Counter
	entUpdateCounter metric.Int64Counter
	entDeleteCounter metric.Int64Counter
)

func init() {
	meter := otel.Meter("spark.db.ent")

	var err error
	entReadCounter, err = meter.Int64Counter(
		"spark_db_ent_operations_read_total",
		metric.WithDescription("Total number of Ent read operations by table"),
		metric.WithUnit("{operations}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	entInsertCounter, err = meter.Int64Counter(
		"spark_db_ent_operations_insert_total",
		metric.WithDescription("Total number of Ent insert operations by table"),
		metric.WithUnit("{operations}"),
	)
	if err != nil {
		otel.Handle(err)
		if entInsertCounter == nil {
			entInsertCounter = noop.Int64Counter{}
		}
	}

	entUpdateCounter, err = meter.Int64Counter(
		"spark_db_ent_operations_update_total",
		metric.WithDescription("Total number of Ent update operations by table"),
		metric.WithUnit("{operations}"),
	)
	if err != nil {
		otel.Handle(err)
		if entUpdateCounter == nil {
			entUpdateCounter = noop.Int64Counter{}
		}
	}

	entDeleteCounter, err = meter.Int64Counter(
		"spark_db_ent_operations_delete_total",
		metric.WithDescription("Total number of Ent delete operations by table"),
		metric.WithUnit("{operations}"),
	)
	if err != nil {
		otel.Handle(err)
		if entDeleteCounter == nil {
			entDeleteCounter = noop.Int64Counter{}
		}
	}
}

// DatabaseStatsInterceptor tracks query (read) operations and their duration
func DatabaseStatsInterceptor(threshold time.Duration) ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			start := time.Now()
			result, err := next.Query(ctx, query)
			duration := time.Since(start)

			tableName := extractTableName(reflect.TypeOf(query).Elem().Name())
			logging.ObserveQuery(ctx, tableName, duration)

			// Track read operation metrics (queries only, not mutations)
			attrs := []attribute.KeyValue{
				attribute.String("table", tableName),
				attribute.Bool("error", err != nil),
			}

			if entReadCounter != nil {
				entReadCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
			}

			return result, err
		})
	})
}

// DatabaseOperationsHook tracks mutation operations (insert, update, delete)
// This should be registered as a global hook on the Ent client.
func DatabaseOperationsHook() ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			result, err := next.Mutate(ctx, mutation)

			// Track mutation metrics
			tableName := extractTableName(mutation.Type())
			attrs := []attribute.KeyValue{
				attribute.String("table", tableName),
				attribute.Bool("error", err != nil),
			}

			op := mutation.Op()
			switch op {
			case OpCreate:
				entInsertCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
			case OpUpdate, OpUpdateOne:
				entUpdateCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
			case OpDelete, OpDeleteOne:
				entDeleteCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
			}

			return result, err
		})
	}
}

// extractTableName extracts the table name from the query type name
// Examples:
//   - "TransferQuery" -> "transfer"
//   - "TokenOutputQuery" -> "token_output"
func extractTableName(queryType string) string {
	// Remove "Query" or "Mutation" suffix
	name := strings.TrimSuffix(queryType, "Query")
	name = strings.TrimSuffix(name, "Mutation")

	// Convert from CamelCase to snake_case
	var result strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('_')
		}
		result.WriteRune(r)
	}

	return strings.ToLower(result.String())
}
