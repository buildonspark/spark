package logging

import (
	"context"
	"log/slog"
	"time"
)

type dbStatsContextKey string

const dbStatsKey = dbStatsContextKey("dbStats")

type dbStats struct {
	queryCount    int
	queryDuration time.Duration
}

func InitTable(ctx context.Context) context.Context {
	return context.WithValue(ctx, dbStatsKey, make(map[string]*dbStats))
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

func LogTable(ctx context.Context, duration time.Duration) {
	ctxDbStats, ok := ctx.Value(dbStatsKey).(map[string]*dbStats)
	if !ok {
		return
	}

	result := make(map[string]any)
	result["_table"] = "spark-requests"
	result["duration"] = duration.Seconds()

	totals := dbStats{}

	for table, stats := range ctxDbStats {
		result["database.select."+table+".queries"] = stats.queryCount
		result["database.select."+table+".duration"] = stats.queryDuration.Seconds()

		totals.queryCount += stats.queryCount
		totals.queryDuration += stats.queryDuration
	}

	result["database.select.queries"] = totals.queryCount
	result["database.select.duration"] = totals.queryDuration.Seconds()

	logger := GetLoggerFromContext(ctx)

	attrs := make([]slog.Attr, 0, len(result))
	for key, value := range result {
		attrs = append(attrs, slog.Any(key, value))
	}

	logger.LogAttrs(context.Background(), slog.LevelInfo, "", attrs...)
}
