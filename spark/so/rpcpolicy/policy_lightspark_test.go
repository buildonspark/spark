//go:build lightspark

package rpcpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
)

func init() {
	baseRegisteredServiceDescs = append(baseRegisteredServiceDescs, &pbssp.SparkSspInternalService_ServiceDesc)
}

func TestLightsparkSparkSspInternalPoliciesPresent(t *testing.T) {
	// Verify both classes of SSP-internal methods are represented.
	tests := []struct {
		name         string
		method       string
		wantAuthMode AuthMode
	}{
		{
			name:         "anonymous ops query",
			method:       pbssp.SparkSspInternalService_QueryLostNodes_FullMethodName,
			wantAuthMode: AuthUnauthenticated,
		},
		{
			name:         "session-required SSP flow",
			method:       pbssp.SparkSspInternalService_QueryLightningSwapTransfer_FullMethodName,
			wantAuthMode: AuthSession,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := LookUp(tc.method)
			require.True(t, ok)
			assert.Equal(t, tc.wantAuthMode, p.AuthMode)
			assert.True(t, p.InternalOnly)
		})
	}
}
