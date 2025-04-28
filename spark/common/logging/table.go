package logging

import (
	"context"
	"log/slog"
	"time"
)

type dbStatsContextKey string

type serviceStatsContextKey string

const dbStatsKey = dbStatsContextKey("dbStats")

const serviceStatsKey = serviceStatsContextKey("serviceStats")

type dbStats struct {
	queryCount    int
	queryDuration time.Duration
}

type serviceStats struct {
	serviceRequestCount    int
	serviceRequestDuration time.Duration
}

func InitTable(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, dbStatsKey, make(map[string]*dbStats))
	return context.WithValue(ctx, serviceStatsKey, make(map[string]*serviceStats))
}

func ObserveQuery(ctx context.Context, table string, duration time.Duration) {
	stats, ok := ctx.Value(dbStatsKey).(map[string]*dbStats)
	if !ok {
		return
	}

	if _, exists := stats[table]; !exists {
		stats[table] = new(dbStats)
	}

	stats[table].queryCount++
	stats[table].queryDuration += duration
}

func ObserveServiceCall(ctx context.Context, method string, duration time.Duration) {
	stats, ok := ctx.Value(serviceStatsKey).(map[string]*serviceStats)
	if !ok {
		return
	}

	if _, exists := stats[method]; !exists {
		stats[method] = new(serviceStats)
	}

	stats[method].serviceRequestCount++
	stats[method].serviceRequestDuration += duration
}

func LogTable(ctx context.Context, duration time.Duration) {
	result := make(map[string]any)
	fillDbStats(ctx, result)
	fillServiceStats(ctx, result)

	result["_table"] = "spark-requests"
	result["duration"] = duration.Seconds()

	logger := GetLoggerFromContext(ctx)

	attrs := make([]slog.Attr, 0, len(result))
	for key, value := range result {
		attrs = append(attrs, slog.Any(key, value))
	}

	logger.LogAttrs(context.Background(), slog.LevelInfo, "", attrs...)
}

func fillDbStats(ctx context.Context, result map[string]any) {
	ctxDbStats, ok := ctx.Value(dbStatsKey).(map[string]*dbStats)
	if !ok {
		return
	}

	totals := dbStats{}

	for table, stats := range ctxDbStats {
		result["database.select."+table+".queries"] = stats.queryCount
		result["database.select."+table+".duration"] = stats.queryDuration.Seconds()

		totals.queryCount += stats.queryCount
		totals.queryDuration += stats.queryDuration
	}

	result["database.select.queries"] = totals.queryCount
	result["database.select.duration"] = totals.queryDuration.Seconds()
}

func fillServiceStats(ctx context.Context, result map[string]any) {
	ctxServiceStats, ok := ctx.Value(serviceStatsKey).(map[string]*serviceStats)
	if !ok {
		return
	}

	totals := serviceStats{}

	for service, stats := range ctxServiceStats {
		result["service."+service+".requests"] = stats.serviceRequestCount
		result["service."+service+".duration"] = stats.serviceRequestDuration.Seconds()

		totals.serviceRequestCount += stats.serviceRequestCount
		totals.serviceRequestDuration += stats.serviceRequestDuration
	}

	result["service.requests"] = totals.serviceRequestCount
	result["service.duration"] = totals.serviceRequestDuration.Seconds()
}
