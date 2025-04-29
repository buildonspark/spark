package ent

import (
	"context"
	"reflect"
	"time"

	"entgo.io/ent"
	"github.com/lightsparkdev/spark/common/logging"
)

func DatabaseStatsInterceptor(threshold time.Duration) ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			start := time.Now()
			result, err := next.Query(ctx, query)
			duration := time.Since(start)

			logging.ObserveQuery(ctx, reflect.TypeOf(query).Elem().Name(), duration)

			if duration > threshold {
				logger := logging.GetLoggerFromContext(ctx)
				logger.Warn("[SLOW QUERY]", "ent", reflect.TypeOf(query).Elem().Name(), "duration", duration.String())
			}

			return result, err
		})
	})
}
