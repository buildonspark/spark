package grpc

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/stats"
)

// instrumentedStatsHandler wraps another stats.Handler to add additional timing spans
type instrumentedStatsHandler struct {
	wrapped stats.Handler
}

// NewInstrumentedStatsHandler creates a stats handler that adds detailed timing spans
func NewInstrumentedStatsHandler(wrapped stats.Handler) stats.Handler {
	return &instrumentedStatsHandler{wrapped: wrapped}
}

// RPCTimings stores timing information for an RPC lifecycle
type RPCTimings struct {
	TagRPCTime    time.Time
	LastEventTime time.Time
}

// TagRPC is called at the start of an RPC
func (h *instrumentedStatsHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	// Let the wrapped handler (otelgrpc) create its span first
	ctx = h.wrapped.TagRPC(ctx, info)

	// Store timings for gap measurement
	timings := &RPCTimings{
		TagRPCTime:    time.Now(),
		LastEventTime: time.Now(),
	}
	ctx = context.WithValue(ctx, RPCTimingsContextKey, timings)

	return ctx
}

// HandleRPC processes the RPC stats
func (h *instrumentedStatsHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	timings, _ := ctx.Value(RPCTimingsContextKey).(*RPCTimings)

	// Add detailed spans for different phases
	switch stat := s.(type) {
	case *stats.InHeader:
		if timings != nil {
			_, gapSpan := tracer.Start(ctx, "grpc.gap.BeforeInHeader",
				trace.WithTimestamp(timings.LastEventTime))
			gapSpan.End(trace.WithTimestamp(time.Now()))
			timings.LastEventTime = time.Now()
		}

		_, span := tracer.Start(ctx, "grpc.InHeader")
		defer span.End()

	case *stats.InPayload:
		if timings != nil {
			gapStart := timings.LastEventTime
			_, gapSpan := tracer.Start(ctx, "grpc.gap.BeforeInPayload",
				trace.WithTimestamp(gapStart))
			gapSpan.End(trace.WithTimestamp(time.Now()))
			timings.LastEventTime = time.Now()
		}

		_, span := tracer.Start(ctx, "grpc.InPayload")
		defer span.End()
		span.SetAttributes(attribute.Int("payload.length", stat.Length))

	case *stats.Begin:
		if timings != nil {
			gapStart := timings.LastEventTime
			_, gapSpan := tracer.Start(ctx, "grpc.gap.BeforeBegin",
				trace.WithTimestamp(gapStart))
			gapSpan.End(trace.WithTimestamp(time.Now()))
			timings.LastEventTime = time.Now()
		}

		_, span := tracer.Start(ctx, "grpc.Begin")
		defer span.End()
	}

	h.wrapped.HandleRPC(ctx, s)
}

// TagConn is called when a connection is established
func (h *instrumentedStatsHandler) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return h.wrapped.TagConn(ctx, info)
}

// HandleConn processes connection stats
func (h *instrumentedStatsHandler) HandleConn(ctx context.Context, s stats.ConnStats) {
	h.wrapped.HandleConn(ctx, s)
}

type contextKey string

// RPCTimingsContextKey is the context key for storing RPC timing information
const RPCTimingsContextKey contextKey = "rpcTimings"
