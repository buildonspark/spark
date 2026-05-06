package consensus

import (
	"context"
	"fmt"
)

// engineCtxKey is a private, typed context key under which the per-process
// *TwoPCEngine instance is stored. The engine is constructed once at server
// init (where the unwrapped *ent.Client lives) and injected into every
// request ctx by a top-level interceptor — handlers fetch it via GetEngine
// rather than constructing one per call.
type engineCtxKey struct{}

// InjectEngine returns a child context carrying the supplied engine. Should
// be called once per request from the gRPC interceptor chain; handlers
// retrieve the engine via GetEngine.
func InjectEngine(ctx context.Context, engine *TwoPCEngine) context.Context {
	return context.WithValue(ctx, engineCtxKey{}, engine)
}

// GetEngine returns the *TwoPCEngine attached to ctx by the consensus
// engine interceptor. Returns an error if no engine is present, which
// indicates a wiring bug (the interceptor was skipped for this RPC path).
func GetEngine(ctx context.Context) (*TwoPCEngine, error) {
	if engine, ok := ctx.Value(engineCtxKey{}).(*TwoPCEngine); ok && engine != nil {
		return engine, nil
	}
	return nil, fmt.Errorf("no consensus engine in context (engine interceptor missing?)")
}
