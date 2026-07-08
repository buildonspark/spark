package consensus

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
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

// coordinatorIdentityCtxKey carries the identity public key of the SO that is
// coordinating the in-flight 2PC operation, resolved from the engine's
// authenticated coordinator_index on the participant side (DispatchPrepare).
type coordinatorIdentityCtxKey struct{}

// WithCoordinatorIdentity attaches the coordinating SO's identity public key to
// ctx. Participant Prepare handlers read it via CoordinatorIdentityFromContext to
// record the coordinator on durable state without trusting a self-declared
// payload field — the key is derived from the engine's coordinator_index, so it
// is always a real signing operator.
func WithCoordinatorIdentity(ctx context.Context, identityPublicKey keys.Public) context.Context {
	return context.WithValue(ctx, coordinatorIdentityCtxKey{}, identityPublicKey)
}

// CoordinatorIdentityFromContext returns the coordinating SO's identity public
// key attached by WithCoordinatorIdentity. The second return is false on the
// coordinator's own self-Prepare path, which the engine invokes directly without
// going through the ConsensusPrepare RPC (the running SO is the coordinator
// there).
func CoordinatorIdentityFromContext(ctx context.Context) (keys.Public, bool) {
	identityPublicKey, ok := ctx.Value(coordinatorIdentityCtxKey{}).(keys.Public)
	return identityPublicKey, ok
}
