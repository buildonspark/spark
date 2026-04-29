package grpcutil

import "context"

type grpcWebRequestKey struct{}

// IsGrpcWebRequest reports whether ctx belongs to an RPC that arrived via the
// gRPC-web wrapper. Handlers can use this to take gRPC-web-specific actions
// (e.g. proactive cancellation on shutdown, since gRPC-web has no HTTP/2
// GOAWAY equivalent that the JS client honors).
func IsGrpcWebRequest(ctx context.Context) bool {
	v, _ := ctx.Value(grpcWebRequestKey{}).(bool)
	return v
}

// WithGrpcWebRequest returns ctx marked as belonging to a gRPC-web request.
// In production this is called from the HTTP handler that fronts the gRPC-web
// wrapper; tests can use it to construct contexts that simulate gRPC-web
// traffic.
func WithGrpcWebRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, grpcWebRequestKey{}, true)
}
