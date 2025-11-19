package grpc

import (
	"context"
	"time"

	"github.com/lightsparkdev/spark/so/grpcutil"
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
	MethodName    string
}

// endSpanAndUpdateTiming ends the span and updates the last event time
func endSpanAndUpdateTiming(span trace.Span, timings *RPCTimings) {
	span.End()
	if timings != nil {
		timings.LastEventTime = time.Now()
	}
}

// setRPCMethodAttributes sets the RPC method attributes on a span
func setRPCMethodAttributes(span trace.Span, timings *RPCTimings, additionalAttrs ...attribute.KeyValue) {
	if timings == nil {
		return
	}
	attrs := additionalAttrs
	if methodAttrs := grpcutil.ParseFullMethod(timings.MethodName); methodAttrs != nil {
		attrs = append(attrs, methodAttrs...)
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
}

// TagRPC is called at the start of an RPC
func (h *instrumentedStatsHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	// Let the wrapped handler (otelgrpc) create its span first
	ctx = h.wrapped.TagRPC(ctx, info)

	now := time.Now()
	// Store timings for gap measurement
	timings := &RPCTimings{
		TagRPCTime:    now,
		LastEventTime: now,
		MethodName:    info.FullMethodName,
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
			now := time.Now()
			_, gapSpan := tracer.Start(ctx, "grpc.gap.BeforeInHeader",
				trace.WithTimestamp(timings.LastEventTime))
			setRPCMethodAttributes(gapSpan, timings)
			gapSpan.End(trace.WithTimestamp(now))
		}

		_, span := tracer.Start(ctx, "grpc.InHeader")
		setRPCMethodAttributes(span, timings)
		defer endSpanAndUpdateTiming(span, timings)

	case *stats.InPayload:
		if timings != nil {
			now := time.Now()
			_, gapSpan := tracer.Start(ctx, "grpc.gap.BeforeInPayload",
				trace.WithTimestamp(timings.LastEventTime))
			setRPCMethodAttributes(gapSpan, timings)
			gapSpan.End(trace.WithTimestamp(now))
		}

		_, span := tracer.Start(ctx, "grpc.InPayload")
		defer endSpanAndUpdateTiming(span, timings)
		setRPCMethodAttributes(span, timings, attribute.Int("payload.length", stat.Length))

	case *stats.Begin:
		if timings != nil {
			now := time.Now()
			_, gapSpan := tracer.Start(ctx, "grpc.gap.BeforeBegin",
				trace.WithTimestamp(timings.LastEventTime))
			setRPCMethodAttributes(gapSpan, timings)
			gapSpan.End(trace.WithTimestamp(now))
		}

		_, span := tracer.Start(ctx, "grpc.Begin")
		setRPCMethodAttributes(span, timings)
		defer endSpanAndUpdateTiming(span, timings)
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
