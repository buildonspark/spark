package grpcutil

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

// ParseFullMethod builds standard RPC attributes (service, method) from a gRPC FullMethod string.
// Following OpenTelemetry semantic conventions.
func ParseFullMethod(fullMethod string) []attribute.KeyValue {
	service, method, ok := splitFullMethod(fullMethod)
	if !ok {
		return nil
	}

	var attrs []attribute.KeyValue
	if service != "" {
		attrs = append(attrs, semconv.RPCService(service))
	}
	if method != "" {
		attrs = append(attrs, semconv.RPCMethod(method))
	}
	return attrs
}

// splitFullMethod parses a gRPC FullMethod string and returns (service, method, ok).
// ok=false if the input is invalid or not in the expected format.
func splitFullMethod(fullMethod string) (string, string, bool) {
	if !strings.HasPrefix(fullMethod, "/") {
		return "", "", false
	}
	name := fullMethod[1:]
	pos := strings.LastIndex(name, "/")
	if pos < 0 {
		return "", "", false
	}
	service, method := name[:pos], name[pos+1:]
	return service, method, true
}

// ParseFullMethodStrings is the exported helper returning service and method.
// Returns empty strings if parsing fails.
func ParseFullMethodStrings(fullMethod string) (string, string) {
	service, method, ok := splitFullMethod(fullMethod)
	if !ok {
		return "", ""
	}
	return service, method
}
