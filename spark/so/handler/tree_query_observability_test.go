package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/knobs"
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

// bindAncestorChainMetricReader rebinds both ancestor-chain histograms to a manual reader for
// the duration of the test (the same pattern as gossip_handler_test.go's
// TestConsensusFenceMetricDispositions and so/middleware/rate_limit_test.go).
func bindAncestorChainMetricReader(t *testing.T) *msdk.ManualReader {
	t.Helper()
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
	return reader
}

// ancestorChainLabels is the attribute pair the two paths are compared by in production.
type ancestorChainLabels struct {
	path    string
	success bool
}

// collectAncestorChainDurationLabels returns how many duration data points were recorded under
// each (path, success) label pair.
func collectAncestorChainDurationLabels(t *testing.T, ctx context.Context, reader *msdk.ManualReader) map[ancestorChainLabels]int {
	t.Helper()
	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	labels := make(map[ancestorChainLabels]int)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "spark_tree_query_ancestor_chain_duration" {
				continue
			}
			require.IsType(t, md.Histogram[float64]{}, m.Data)
			hs, _ := m.Data.(md.Histogram[float64])
			for _, dp := range hs.DataPoints {
				path, _ := dp.Attributes.Value(attribute.Key("path"))
				success, _ := dp.Attributes.Value(attribute.Key("success"))
				labels[ancestorChainLabels{path.AsString(), success.AsBool()}] += int(dp.Count)
			}
		}
	}
	return labels
}

// TestQueryNodes_AncestorChainMetrics pins the ancestor-chain metrics' registration,
// labels, and values end to end through a real QueryNodes call.
func TestQueryNodes_AncestorChainMetrics(t *testing.T) {
	// isSSP=true below means the root is always included regardless of createTime, so any
	// timestamp works here.
	ctx, root, parent, leaf := createAncestorChainTestNodes(t, time.Now())

	reader := bindAncestorChainMetricReader(t)

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

// TestQueryNodes_AncestorChainKnobDispatch is the test that makes the two paths comparable: the
// knob has to pick the implementation AND label the metric with the one it picked, or a split
// rollout silently attributes batched latency to "legacy". It also asserts the two paths return
// the same nodes end to end, so a latency difference between the labels can only be the walk.
// Postgres-backed because the batched path's recursive CTE isn't SQLite-compatible.
func TestQueryNodes_AncestorChainKnobDispatch(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{20})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Mainnet)
	root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, postCutoff, 300000, 0)
	branch := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, postCutoff, 200000, 0)
	leaf := createBatchedTestNode(t, ctx, tc.Client, rng, tree, branch, keyshare, owner, postCutoff, 100000, 0)

	handler := NewTreeQueryHandler(&so.Config{})
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{NodeIds: []string{leaf.ID.String()}},
		},
		IncludeParents: true,
	}

	queryWithKnob := func(t *testing.T, rollout float64) (*pb.QueryNodesResponse, map[ancestorChainLabels]int) {
		t.Helper()
		reader := bindAncestorChainMetricReader(t)
		knobCtx := knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobUseBatchedAncestorChain: rollout,
		}))
		resp, err := handler.QueryNodes(knobCtx, req, true)
		require.NoError(t, err)
		return resp, collectAncestorChainDurationLabels(t, ctx, reader)
	}

	var legacyResp, batchedResp *pb.QueryNodesResponse

	t.Run("knob off routes legacy", func(t *testing.T) {
		resp, labels := queryWithKnob(t, 0)
		legacyResp = resp
		assert.Equal(t, map[ancestorChainLabels]int{{ancestorChainPathLegacy, true}: 1}, labels)
	})

	t.Run("knob on routes batched", func(t *testing.T) {
		resp, labels := queryWithKnob(t, 100)
		batchedResp = resp
		assert.Equal(t, map[ancestorChainLabels]int{{ancestorChainPathBatched, true}: 1}, labels)
	})

	require.NotNil(t, legacyResp)
	require.NotNil(t, batchedResp)
	requireTreeNodeMapsEqual(t, legacyResp.GetNodes(), batchedResp.GetNodes())
	assert.Contains(t, batchedResp.GetNodes(), leaf.ID.String())
	assert.Contains(t, batchedResp.GetNodes(), branch.ID.String())
	assert.Contains(t, batchedResp.GetNodes(), root.ID.String())
}

// TestQueryNodes_AncestorChainMetricsOnFailure pins that a batched-path failure is still recorded,
// labelled with the path that failed. Without this the error rates of the two paths aren't
// comparable: a batched-only failure mode (the depth bound) would be invisible or, worse,
// attributed to legacy.
func TestQueryNodes_AncestorChainMetricsOnFailure(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{23})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	originalMaxDepth := ancestorChainMaxDepth
	ancestorChainMaxDepth = 1
	t.Cleanup(func() { ancestorChainMaxDepth = originalMaxDepth })

	tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Regtest)
	var leaf *ent.TreeNode
	for i := 0; i <= ancestorChainMaxDepth+2; i++ {
		leaf = createBatchedTestNode(t, ctx, tc.Client, rng, tree, leaf, keyshare, owner, postCutoff, 100000, int16(i))
	}

	reader := bindAncestorChainMetricReader(t)
	knobCtx := knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobUseBatchedAncestorChain: 100,
	}))

	handler := NewTreeQueryHandler(&so.Config{})
	resp, err := handler.QueryNodes(knobCtx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{NodeIds: []string{leaf.ID.String()}},
		},
		IncludeParents: true,
	}, true)
	require.Error(t, err, "the depth bound must surface out of QueryNodes, not be swallowed")
	assert.Nil(t, resp)

	labels := collectAncestorChainDurationLabels(t, ctx, reader)
	assert.Equal(t, map[ancestorChainLabels]int{{ancestorChainPathBatched, false}: 1}, labels)
}
