package logging

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

type ClientInfoProvider interface {
	GetClientIP(ctx context.Context) (string, error)
}

type TableLogger struct {
	clientInfo ClientInfoProvider
}

func NewTableLogger(clientInfo ClientInfoProvider) *TableLogger {
	return &TableLogger{
		clientInfo: clientInfo,
	}
}

func (t *TableLogger) Log(
	ctx context.Context,
	duration time.Duration,
	req proto.Message,
	res proto.Message,
	err error,
) {
	result := make(map[string]any)
	fillDbStats(ctx, result)
	fillServiceStats(ctx, result)

	result["_table"] = "spark-requests"
	result["duration"] = duration.Seconds()

	if req != nil {
		reqJSON, err := FormatProtoMessage(req)
		if err != nil {
			result["request.message"] = err.Error()
		} else {
			result["request.message"] = reqJSON
			result["request.length"] = len(reqJSON)
		}
	}

	if res != nil {
		resJSON, err := FormatProtoMessage(res)
		if err != nil {
			result["response.message"] = err.Error()
		} else {
			result["response.message"] = resJSON
			result["response.length"] = len(resJSON)
		}
	}

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

	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if len(md.Get("origin")) > 0 {
			result["grpc.client.origin"] = md.Get("origin")[0]
		}

		if len(md.Get("user-agent")) > 0 {
			result["grpc.client.user_agent"] = md.Get("user-agent")[0]
		}

		if len(md.Get("x-client-env")) > 0 {
			result["grpc.client.client_env"] = md.Get("x-client-env")[0]
		}
	}

	if t.clientInfo != nil {
		if clientIP, err := t.clientInfo.GetClientIP(ctx); err == nil {
			result["grpc.client.ip"] = clientIP
		}
	}

	logger := GetLoggerFromContext(ctx)

	attrs := make([]zap.Field, 0, len(result))
	for key, value := range result {
		attrs = append(attrs, zap.Any(key, value))
	}

	logger.Info("", attrs...)
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
