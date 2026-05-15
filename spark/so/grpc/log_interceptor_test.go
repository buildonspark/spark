package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"

	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// findSparkRequestsEntry returns the structured log entry the table logger
// emits (identified by _table:"spark-requests").
func findSparkRequestsEntry(t *testing.T, logs *observer.ObservedLogs) map[string]any {
	t.Helper()
	for _, entry := range logs.All() {
		fields := entry.ContextMap()
		if table, _ := fields["_table"].(string); table == "spark-requests" {
			return fields
		}
	}
	t.Fatal("no spark-requests log entry emitted")
	return nil
}

func TestLogInterceptor_OmitsResponseBodyForHighVolumeQuery(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	rootLogger := zap.New(core)
	tableLogger := logging.NewTableLogger(nil)
	interceptor := LogInterceptor(rootLogger, tableLogger)

	handler := func(ctx context.Context, req any) (any, error) {
		return &pb.QueryNodesResponse{}, nil
	}

	_, err := interceptor(
		t.Context(),
		&pb.QueryNodesRequest{},
		&grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/query_nodes"},
		handler,
	)
	require.NoError(t, err)

	entry := findSparkRequestsEntry(t, logs)
	assert.Equal(t, "MSG_OMITTED", entry["response.message"],
		"response.message should be the omitted-placeholder for /spark.SparkService/query_nodes")
	_, hasLen := entry["response.length"]
	assert.True(t, hasLen, "response.length should still be recorded for size visibility")
	_, hasReqMsg := entry["request.message"]
	assert.True(t, hasReqMsg, "request.message must still be logged so the call is replayable")
}

func TestLogInterceptor_KeepsResponseBodyForNonListedMethod(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	rootLogger := zap.New(core)
	tableLogger := logging.NewTableLogger(nil)
	interceptor := LogInterceptor(rootLogger, tableLogger)

	handler := func(ctx context.Context, req any) (any, error) {
		return &pb.StartTransferResponse{}, nil
	}

	_, err := interceptor(
		t.Context(),
		&pb.StartTransferRequest{},
		&grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/start_transfer_v2"},
		handler,
	)
	require.NoError(t, err)

	entry := findSparkRequestsEntry(t, logs)
	msg, hasMsg := entry["response.message"]
	assert.True(t, hasMsg, "response.message must be present for mutating RPCs")
	assert.NotEqual(t, "MSG_OMITTED", msg, "non-deny-listed methods must not get the placeholder")
}
