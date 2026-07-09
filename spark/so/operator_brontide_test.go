package so

import (
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// testConnFactory hands out real (but non-connecting) client conns for both the brontide and plain-TLS paths.
type testConnFactory struct {
	t *testing.T
}

func (f *testConnFactory) NewGRPCConnection(_ string, _ *common.RetryPolicyConfig, _ *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	return createTestConnection(f.t), nil
}

func (s *SigningOperator) hasPool(key connPoolKey) bool {
	s.connPoolsMu.Lock()
	defer s.connPoolsMu.Unlock()
	_, ok := s.connPools[key]
	return ok
}

// TestBrontidePoolEvictedWhenKnobFlipsOff verifies the kill-switch actually tears down the brontide pool: once the
// knob is off, the internal-connection path must drop the previously-created brontide pool rather than leaving its
// connections alive for the process lifetime.
func TestBrontidePoolEvictedWhenKnobFlipsOff(t *testing.T) {
	factory := &testConnFactory{t: t}
	op := &SigningOperator{
		AddressRpc:                "rpc-addr",
		InternalAddress:           "internal-addr",
		brontideAvailable:         true,
		internalConnFactory:       factory,
		OperatorConnectionFactory: factory,
		connPoolConfig:            DefaultOperatorConnPoolConfig(),
	}
	brontideKey := connPoolKey{transport: transportBrontide, address: op.InternalAddress}

	onCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobInternalRPCBrontideEnabled: 1,
	}))
	conn, err := op.NewOperatorInternalGRPCConnection(onCtx)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.True(t, op.hasPool(brontideKey), "brontide pool should exist while the knob is on")

	offCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{}))
	conn, err = op.NewOperatorInternalGRPCConnection(offCtx)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.False(t, op.hasPool(brontideKey), "brontide pool should be evicted once the knob is off")
}

// TestBrontidePoolNotEvictedWhenUnavailable ensures the eviction path is inert for plain-TLS-only operators: with
// brontide never provisioned, an internal call must not touch (or create) a brontide pool.
func TestBrontidePoolNotEvictedWhenUnavailable(t *testing.T) {
	factory := &testConnFactory{t: t}
	op := &SigningOperator{
		AddressRpc:                "rpc-addr",
		InternalAddress:           "internal-addr",
		OperatorConnectionFactory: factory,
		connPoolConfig:            DefaultOperatorConnPoolConfig(),
	}

	offCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{}))
	conn, err := op.NewOperatorInternalGRPCConnection(offCtx)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	require.False(t, op.hasPool(connPoolKey{transport: transportBrontide, address: op.InternalAddress}))
	require.True(t, op.hasPool(connPoolKey{transport: transportTLS, address: op.AddressRpc}),
		"internal call should have fallen through to the plain-TLS AddressRpc pool")
}
