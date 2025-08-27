package grpc

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

// ParseFullMethod  all applicable span or metric attribute.KeyValue attributes based
// on a gRPC's FullMethod, following OpenTelemetry semantic conventions.
//
// Taken from go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc/internal
func ParseFullMethod(fullMethod string) []attribute.KeyValue {
	if !strings.HasPrefix(fullMethod, "/") {
		// Invalid format, does not follow `/package.service/method`.
		return nil
	}
	name := fullMethod[1:]
	pos := strings.LastIndex(name, "/")
	if pos < 0 {
		// Invalid format, does not follow `/package.service/method`.
		return nil
	}
	service, method := name[:pos], name[pos+1:]

	var attrs []attribute.KeyValue
	if service != "" {
		attrs = append(attrs, semconv.RPCService(service))
	}
	if method != "" {
		attrs = append(attrs, semconv.RPCMethod(method))
	}
	return attrs
}
