package grpc

import (
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("grpc")
