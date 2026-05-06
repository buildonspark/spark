package grpc

import (
	"context"

	"github.com/lightsparkdev/spark/so/consensus"
	"google.golang.org/grpc"
)

// ConsensusEngineInterceptor injects the per-process *consensus.TwoPCEngine
// into every request ctx so handlers can drive consensus operations via
// consensus.GetEngine(ctx).Execute(...) without constructing the engine
// themselves. The engine is stateless across requests — it just carries
// the operator config, the gossip sender, and the unwrapped *ent.Client
// used for engine bookkeeping writes — so a single instance is shared by
// every request on this operator.
//
// Putting this on the interceptor chain (rather than wiring the engine
// through every handler constructor) keeps handler signatures focused on
// their own dependencies and lets layer-1's request-transaction-independent
// bookkeeping stay an internal engine detail rather than leaking the
// raw *ent.Client into handler code.
func ConsensusEngineInterceptor(engine *consensus.TwoPCEngine) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(consensus.InjectEngine(ctx, engine), req)
	}
}
