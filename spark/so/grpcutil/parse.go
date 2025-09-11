package grpcutil

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

// ParseFullMethod builds standard RPC attributes (service, method) from a gRPC FullMethod string.
// Following OpenTelemetry semantic conventions.
func ParseFullMethod(fullMethod string) []attribute.KeyValue {
	if !strings.HasPrefix(fullMethod, "/") {
		return nil
	}
	name := fullMethod[1:]
	pos := strings.LastIndex(name, "/")
	if pos < 0 {
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
