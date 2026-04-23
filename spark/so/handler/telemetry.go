package handler

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("handler")

func endSpanWithError(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
	}
	span.End()
}
