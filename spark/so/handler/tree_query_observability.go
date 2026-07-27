package handler

import (
	"context"
	"time"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const ancestorChainPathLegacy = "legacy"

var ancestorChainDuration metric.Float64Histogram
var ancestorChainAdditionalNodeCount metric.Float64Histogram

func init() {
	ancestorChainDuration = newAncestorChainDurationHistogram()
	ancestorChainAdditionalNodeCount = newAncestorChainAdditionalNodeCountHistogram()
}

// newAncestorChainDurationHistogram builds the duration histogram from the current global meter
// provider; extracted (like gossip_handler.go's newConsensusOpFencedCounter) so tests can rebind
// it to a manual reader.
func newAncestorChainDurationHistogram() metric.Float64Histogram {
	h, err := otel.GetMeterProvider().Meter("handler.tree_query").Float64Histogram(
		"spark_tree_query_ancestor_chain_duration",
		metric.WithDescription("Duration of QueryNodes' ancestor-chain resolution (getAncestorChain), summed across all requested nodes in one request"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000),
	)
	if err != nil {
		otel.Handle(err)
		if h == nil {
			h = noop.Float64Histogram{}
		}
	}
	return h
}

// newAncestorChainAdditionalNodeCountHistogram builds the additional-node-count histogram from
// the current global meter provider; extracted so tests can rebind it to a manual reader.
func newAncestorChainAdditionalNodeCountHistogram() metric.Float64Histogram {
	h, err := otel.GetMeterProvider().Meter("handler.tree_query").Float64Histogram(
		"spark_tree_query_ancestor_chain_additional_node_count",
		metric.WithDescription("Number of unique ancestor nodes returned by a QueryNodes ancestor-chain request in addition to the requested nodes themselves. Ancestors shared across requested nodes, or that are themselves requested nodes, are deduplicated and not counted."),
		metric.WithUnit("{count}"),
		metric.WithExplicitBucketBoundaries(0, 1, 5, 10, 25, 50, 100, 250, 500, 1000),
	)
	if err != nil {
		otel.Handle(err)
		if h == nil {
			h = noop.Float64Histogram{}
		}
	}
	return h
}

// additionalAncestorNodeCount returns how many entries in protoNodeMap are not in
// requestedNodeIDs. It counts by membership rather than by len(protoNodeMap)-len(requestedNodeIDs):
// on an early error, protoNodeMap is only ever partially populated (the request loop hasn't
// reached every requested node yet) while requestedNodeIDs is prepopulated with every requested
// node up front, so a size-difference shortcut can go negative. Membership counting stays correct
// and non-negative regardless of how far the request got.
func additionalAncestorNodeCount(protoNodeMap map[string]*pb.TreeNode, requestedNodeIDs map[string]struct{}) int {
	count := 0
	for id := range protoNodeMap {
		if _, requested := requestedNodeIDs[id]; !requested {
			count++
		}
	}
	return count
}

// recordAncestorChainDuration records how long QueryNodes spent resolving ancestor chains for one
// request. `path` distinguishes the resolution strategy (e.g. "legacy" for the per-node recursive
// walk) so a future alternative implementation can be recorded under the same metric name and
// compared directly by slicing on that label. `additionalNodeCount` is the number of unique nodes
// present in the response beyond the requested nodes themselves, not a count of traversal steps:
// a shared or already-requested ancestor is walked (and costs DB round trips) but does not add to
// this count.
func recordAncestorChainDuration(ctx context.Context, path string, elapsed time.Duration, additionalNodeCount int, err error) {
	attrs := metric.WithAttributes(
		attribute.String("path", path),
		attribute.Bool("success", err == nil),
	)
	ancestorChainDuration.Record(ctx, elapsed.Seconds()*1000, attrs)
	ancestorChainAdditionalNodeCount.Record(ctx, float64(additionalNodeCount), attrs)
}
