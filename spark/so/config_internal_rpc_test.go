package so

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInternalRPCConfigNormalize_DefaultsEmptyTransportToTLS(t *testing.T) {
	t.Parallel()
	cfg := InternalRPCConfig{}

	require.NoError(t, cfg.normalize())
	assert.Equal(t, InternalRPCTransportTLS, cfg.Transport)
}

func TestInternalRPCConfigNormalize_AcceptsKnownTransports(t *testing.T) {
	t.Parallel()
	for _, transport := range []InternalRPCTransport{InternalRPCTransportTLS, InternalRPCTransportBrontide} {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()
			cfg := InternalRPCConfig{Transport: transport}

			require.NoError(t, cfg.normalize())
			assert.Equal(t, transport, cfg.Transport)
		})
	}
}

func TestInternalRPCConfigNormalize_RejectsUnrecognizedTransport(t *testing.T) {
	t.Parallel()
	for _, transport := range []InternalRPCTransport{"noise", "TLS", "Brontide", " tls"} {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()
			cfg := InternalRPCConfig{Transport: transport}

			require.ErrorContains(t, cfg.normalize(), "internal_rpc.transport must be")
		})
	}
}
