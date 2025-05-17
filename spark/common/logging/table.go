package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc/status"
)

type dbStatsContextKey string

type serviceStatsContextKey string

const dbStatsKey = dbStatsContextKey("dbStats")

const serviceStatsKey = serviceStatsContextKey("serviceStats")

type dbStatsMap struct {
	stats map[string]*dbStats
	mu    sync.Mutex
}

type dbStats struct {
	queryCount    int
	queryDuration time.Duration
}

type serviceStatsMap struct {
	stats map[string]*serviceStats
	mu    sync.Mutex
}

type serviceStats struct {
	serviceRequestCount    int
	serviceRequestDuration time.Duration
}

func InitTable(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, dbStatsKey, &dbStatsMap{
		stats: make(map[string]*dbStats),
		mu:    sync.Mutex{},
	})
	return context.WithValue(ctx, serviceStatsKey, &serviceStatsMap{
		stats: make(map[string]*serviceStats),
		mu:    sync.Mutex{},
	})
}

func ObserveQuery(ctx context.Context, table string, duration time.Duration) {
	statsMap, ok := ctx.Value(dbStatsKey).(*dbStatsMap)
	if !ok {
		return
	}
	statsMap.mu.Lock()
	defer statsMap.mu.Unlock()

	if _, exists := statsMap.stats[table]; !exists {
		statsMap.stats[table] = new(dbStats)
	}

	statsMap.stats[table].queryCount++
	statsMap.stats[table].queryDuration += duration
}

func ObserveServiceCall(ctx context.Context, method string, duration time.Duration) {
	statsMap, ok := ctx.Value(serviceStatsKey).(*serviceStatsMap)
	if !ok {
		return
	}
	statsMap.mu.Lock()
	defer statsMap.mu.Unlock()

	if _, exists := statsMap.stats[method]; !exists {
		statsMap.stats[method] = new(serviceStats)
	}

	statsMap.stats[method].serviceRequestCount++
	statsMap.stats[method].serviceRequestDuration += duration
}

func LogTable(ctx context.Context, duration time.Duration, err error) {
	result := make(map[string]any)
	fillDbStats(ctx, result)
	fillServiceStats(ctx, result)

	result["_table"] = "spark-requests"
	result["duration"] = duration.Seconds()

	if err != nil {
		st, ok := status.FromError(err)
		if ok {
			result["error.code"] = st.Code().String()
			result["error.message"] = st.Message()
		} else {
			result["error.code"] = "Unknown"
			result["error.message"] = err.Error()
		}
	}

	logger := GetLoggerFromContext(ctx)

	attrs := make([]slog.Attr, 0, len(result))
	for key, value := range result {
		attrs = append(attrs, slog.Any(key, value))
	}

	logger.LogAttrs(context.Background(), slog.LevelInfo, "", attrs...)
}

func fillDbStats(ctx context.Context, result map[string]any) {
	ctxDbStats, ok := ctx.Value(dbStatsKey).(*dbStatsMap)
	if !ok {
		return
	}

	totals := dbStats{}

	for table, stats := range ctxDbStats.stats {
		result["database.select."+table+".queries"] = stats.queryCount
		result["database.select."+table+".duration"] = stats.queryDuration.Seconds()

		totals.queryCount += stats.queryCount
		totals.queryDuration += stats.queryDuration
	}

	result["database.select.queries"] = totals.queryCount
	result["database.select.duration"] = totals.queryDuration.Seconds()
}

func fillServiceStats(ctx context.Context, result map[string]any) {
	ctxServiceStats, ok := ctx.Value(serviceStatsKey).(*serviceStatsMap)
	if !ok {
		return
	}

	totals := serviceStats{}

	for service, stats := range ctxServiceStats.stats {
		result["service."+service+".requests"] = stats.serviceRequestCount
		result["service."+service+".duration"] = stats.serviceRequestDuration.Seconds()

		totals.serviceRequestCount += stats.serviceRequestCount
		totals.serviceRequestDuration += stats.serviceRequestDuration
	}

	result["service.requests"] = totals.serviceRequestCount
	result["service.duration"] = totals.serviceRequestDuration.Seconds()
}
