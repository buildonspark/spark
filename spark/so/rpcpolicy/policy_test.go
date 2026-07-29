package rpcpolicy

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbauthn "github.com/lightsparkdev/spark/proto/spark_authn"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
	pbtoken "github.com/lightsparkdev/spark/proto/spark_token"
	pbtokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
)

// baseRegisteredServiceDescs is the set of ServiceDesc values registered on every operator binary. Build-tag-gated
// services (e.g. SparkSspInternalService under lightspark) extend this list in build-tagged test files.
var baseRegisteredServiceDescs = []*grpc.ServiceDesc{
	&pbauthn.SparkAuthnService_ServiceDesc,
	&pbspark.SparkService_ServiceDesc,
	&pbpartner.SparkPartnerService_ServiceDesc,
	&pbinternal.SparkInternalService_ServiceDesc,
	&pbtoken.SparkTokenService_ServiceDesc,
	&pbtokeninternal.SparkTokenInternalService_ServiceDesc,
	&pbdkg.DKGService_ServiceDesc,
	&pbgossip.GossipService_ServiceDesc,
	&pbmock.MockService_ServiceDesc,
}

// extraRegisteredMethods covers methods registered without a generated ServiceDesc.
var extraRegisteredMethods = []string{
	"/grpc.health.v1.Health/Check",
	"/grpc.health.v1.Health/Watch",
	"/grpc.health.v1.Health/List",
}

func registeredServiceDescs() []*grpc.ServiceDesc {
	return append([]*grpc.ServiceDesc{}, baseRegisteredServiceDescs...)
}

func registeredFullMethods(t *testing.T) []string {
	t.Helper()
	out := append([]string{}, extraRegisteredMethods...)
	for _, sd := range registeredServiceDescs() {
		out = append(out, fullMethodsFromServiceDesc(sd)...)
	}
	slices.Sort(out)
	return out
}

func fullMethodsFromServiceDesc(sd *grpc.ServiceDesc) []string {
	out := make([]string, 0, len(sd.Methods)+len(sd.Streams))
	for _, m := range sd.Methods {
		out = append(out, "/"+sd.ServiceName+"/"+m.MethodName)
	}
	for _, s := range sd.Streams {
		out = append(out, "/"+sd.ServiceName+"/"+s.StreamName)
	}
	return out
}

// TestEveryRegisteredMethodHasAPolicy checks that adding a new RPC without registering a policy entry fails CI.
func TestEveryRegisteredMethodHasAPolicy(t *testing.T) {
	var missing []string
	for _, m := range registeredFullMethods(t) {
		if _, ok := LookUp(m); !ok {
			missing = append(missing, m)
		}
	}
	require.Empty(t, missing, "registered gRPC methods missing rpcpolicy entries; add them to rpcpolicy/policy.go: %v", missing)
}

// TestNoOrphanPolicies guards the opposite direction: a policy entry that no longer corresponds to a registered method
// is dead code and likely indicates a stale rename.
func TestNoOrphanPolicies(t *testing.T) {
	registered := map[string]struct{}{}
	for _, m := range registeredFullMethods(t) {
		registered[m] = struct{}{}
	}
	var orphan []string
	for _, m := range RegisteredMethods() {
		if _, ok := registered[m]; !ok {
			orphan = append(orphan, m)
		}
	}
	slices.Sort(orphan)
	require.Empty(t, orphan, "rpcpolicy entries for methods no longer registered on the server: %v", orphan)
}

func TestLookupBehavior(t *testing.T) {
	tests := []struct {
		name                 string
		method               string
		expectedAuthMode     AuthMode
		expectedInternalOnly bool
	}{
		{
			name:             "public unauthenticated query",
			method:           pbspark.SparkService_QueryNodes_FullMethodName,
			expectedAuthMode: AuthUnauthenticated,
		},
		{
			name:             "session-required transfer",
			method:           pbspark.SparkService_StartTransferV3_FullMethodName,
			expectedAuthMode: AuthSession,
		},
		{
			name:             "session-required multiparty transfer",
			method:           pbspark.SparkService_StartTransferMpc_FullMethodName,
			expectedAuthMode: AuthSession,
		},
		{
			name:                 "internal-only SO-to-SO",
			method:               pbinternal.SparkInternalService_FinalizeTransfer_FullMethodName,
			expectedAuthMode:     AuthUnauthenticated,
			expectedInternalOnly: true,
		},
		{
			name:             "auth challenge",
			method:           pbauthn.SparkAuthnService_GetChallenge_FullMethodName,
			expectedAuthMode: AuthUnauthenticated,
		},
		{
			name:             "health probe",
			method:           "/grpc.health.v1.Health/Check",
			expectedAuthMode: AuthUnauthenticated,
		},
		{
			name:             "partner basic-auth query",
			method:           pbpartner.SparkPartnerService_QuerySparkTransactionVolumes_FullMethodName,
			expectedAuthMode: AuthPartnerBasic,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := LookUp(tc.method)
			require.True(t, ok, "policy must exist for %s", tc.method)
			assert.Equal(t, tc.expectedAuthMode, p.AuthMode)
			assert.Equal(t, tc.expectedInternalOnly, p.InternalOnly)
			// Only AuthSession requires a session token; AuthPartnerBasic is authenticated downstream.
			assert.Equal(t, tc.expectedAuthMode == AuthSession, IsAuthenticated(tc.method))
			assert.Equal(t, tc.expectedInternalOnly, IsInternalOnly(tc.method))
		})
	}
}

// TestSparkPartnerServiceIsUnaryOnly guards the AuthPartnerBasic streaming footgun: Basic Auth is
// enforced only on the unary interceptor chain, so a streaming SparkPartnerService method would be
// effectively unauthenticated. If a streaming RPC is ever added here, this fails until the auth model
// is extended to cover it.
func TestSparkPartnerServiceIsUnaryOnly(t *testing.T) {
	require.Empty(t, pbpartner.SparkPartnerService_ServiceDesc.Streams,
		"SparkPartnerService must stay unary-only: AuthPartnerBasic is enforced only on the unary chain "+
			"(BasicAuthInterceptor), so a streaming method would skip Basic Auth verification")
}

func TestUnknownMethodFailsClosed(t *testing.T) {
	_, ok := LookUp("/never.Registered/Method")
	assert.False(t, ok)
	assert.True(t, IsAuthenticated("/never.Registered/Method"), "unknown methods must require authn (fail closed)")
	assert.False(t, IsInternalOnly("/never.Registered/Method"))
}
