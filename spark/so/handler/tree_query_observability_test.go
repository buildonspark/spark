package handler

import (
	"context"
	"testing"
	"time"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestAdditionalAncestorNodeCount(t *testing.T) {
	node := &pb.TreeNode{}

	t.Run("no ancestors added", func(t *testing.T) {
		protoNodeMap := map[string]*pb.TreeNode{"A": node, "B": node}
		requestedNodeIDs := map[string]struct{}{"A": {}, "B": {}}
		assert.Equal(t, 0, additionalAncestorNodeCount(protoNodeMap, requestedNodeIDs))
	})

	t.Run("one ancestor added beyond the requested nodes", func(t *testing.T) {
		protoNodeMap := map[string]*pb.TreeNode{"A": node, "ancestor": node}
		requestedNodeIDs := map[string]struct{}{"A": {}, "B": {}}
		assert.Equal(t, 1, additionalAncestorNodeCount(protoNodeMap, requestedNodeIDs))
	})

	t.Run("ancestor that is itself a requested node is not double-counted", func(t *testing.T) {
		// B is a requested node reached early as A's ancestor, before B's own loop
		// iteration has added it via its own protoNodeMap entry.
		protoNodeMap := map[string]*pb.TreeNode{"A": node, "B": node}
		requestedNodeIDs := map[string]struct{}{"A": {}, "B": {}}
		assert.Equal(t, 0, additionalAncestorNodeCount(protoNodeMap, requestedNodeIDs))
	})

	t.Run("early failure leaves protoNodeMap smaller than requestedNodeIDs without going negative", func(t *testing.T) {
		// Regression test: requestedNodeIDs is prepopulated with every requested node up
		// front, but protoNodeMap only reflects nodes processed so far when a request
		// fails partway through. A naive len(protoNodeMap)-len(requestedNodeIDs) would be
		// negative here (1-2 = -1); membership counting must return 0 instead.
		protoNodeMap := map[string]*pb.TreeNode{"A": node}
		requestedNodeIDs := map[string]struct{}{"A": {}, "B": {}}
		assert.Equal(t, 0, additionalAncestorNodeCount(protoNodeMap, requestedNodeIDs))
	})
}

// TestQueryNodes_AncestorChainMetrics pins the ancestor-chain metrics' registration,
// labels, and values end to end through a real QueryNodes call, using an OTel manual
// reader (the same pattern as gossip_handler_test.go's TestConsensusFenceMetricDispositions
// and so/middleware/rate_limit_test.go).
func TestQueryNodes_AncestorChainMetrics(t *testing.T) {
	// isSSP=true below means the root is always included regardless of createTime, so any
	// timestamp works here.
	ctx, root, parent, leaf := createAncestorChainTestNodes(t, time.Now())

	reader := msdk.NewManualReader()
	prevProvider := otel.GetMeterProvider()
	testProvider := msdk.NewMeterProvider(msdk.WithReader(reader))
	otel.SetMeterProvider(testProvider)
	prevDuration := ancestorChainDuration
	prevCount := ancestorChainAdditionalNodeCount
	ancestorChainDuration = newAncestorChainDurationHistogram()
	ancestorChainAdditionalNodeCount = newAncestorChainAdditionalNodeCountHistogram()
	t.Cleanup(func() {
		ancestorChainDuration = prevDuration
		ancestorChainAdditionalNodeCount = prevCount
		otel.SetMeterProvider(prevProvider)
		// Deliberately not t.Context(): it is cancelled before t.Cleanup runs, so
		// Shutdown needs a live context.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:usetesting // see comment above
		defer cancel()
		require.NoError(t, testProvider.Shutdown(shutdownCtx))
	})

	handler := NewTreeQueryHandler(&so.Config{})
	resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{NodeIds: []string{leaf.ID.String()}},
		},
		IncludeParents: true,
	}, true)
	require.NoError(t, err)
	require.Contains(t, resp.GetNodes(), leaf.ID.String())
	require.Contains(t, resp.GetNodes(), parent.ID.String())
	require.Contains(t, resp.GetNodes(), root.ID.String())

	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	var durationCount int
	var additionalCount int
	var additionalSum float64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "spark_tree_query_ancestor_chain_duration":
				require.IsType(t, md.Histogram[float64]{}, m.Data)
				hs, _ := m.Data.(md.Histogram[float64])
				for _, dp := range hs.DataPoints {
					path, _ := dp.Attributes.Value(attribute.Key("path"))
					success, _ := dp.Attributes.Value(attribute.Key("success"))
					assert.Equal(t, ancestorChainPathLegacy, path.AsString())
					assert.True(t, success.AsBool())
					durationCount += int(dp.Count)
				}
			case "spark_tree_query_ancestor_chain_additional_node_count":
				require.IsType(t, md.Histogram[float64]{}, m.Data)
				hs, _ := m.Data.(md.Histogram[float64])
				for _, dp := range hs.DataPoints {
					additionalCount += int(dp.Count)
					additionalSum += dp.Sum
				}
			}
		}
	}
	assert.Equal(t, 1, durationCount, "one QueryNodes call should record one duration data point")
	assert.Equal(t, 1, additionalCount, "one QueryNodes call should record one additional-node-count data point")
	assert.InDelta(t, 2.0, additionalSum, 0.001, "parent + root are additional beyond the requested leaf")
}
