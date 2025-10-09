package schema

import (
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("ent.schema")
