package helper

import (
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("helper")
