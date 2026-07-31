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

func registeredFullMethods(t *testing.T) []string {
	t.Helper()
	out := slices.Clone(extraRegisteredMethods)
	for _, sd := range baseRegisteredServiceDescs {
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
			expectedAuthMode: AuthAnonymous,
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
			expectedAuthMode:     AuthOperatorBrontide,
			expectedInternalOnly: true,
		},
		{
			name:             "auth challenge",
			method:           pbauthn.SparkAuthnService_GetChallenge_FullMethodName,
			expectedAuthMode: AuthAnonymous,
		},
		{
			name:             "health probe",
			method:           "/grpc.health.v1.Health/Check",
			expectedAuthMode: AuthAnonymous,
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
			assert.Equal(t, tc.expectedAuthMode == AuthSession, RequiresSessionToken(tc.method))
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
		"SparkPartnerService must stay unary-only: AuthPartnerBasic is only enforced on the unary chain, so a streaming method would skip Basic Auth verification")
}

// soToSoServiceDescs mirrors the services registered by RegisterInternalGrpcServers in bin/operator — the ones hosted on
// the brontide-authenticated internal listener. It's a literal because bin/operator is package main and can't be
// imported, so nothing in this package can see the real registration: drift between this list and
// RegisterInternalGrpcServers is caught by TestInternalListenerServicesAreOperatorBrontide in
// bin/operator/internal_services_policy_test.go, which registers into a real grpc.Server and reads the registration back.
var soToSoServiceDescs = []*grpc.ServiceDesc{
	&pbinternal.SparkInternalService_ServiceDesc,
	&pbtokeninternal.SparkTokenInternalService_ServiceDesc,
	&pbgossip.GossipService_ServiceDesc,
	&pbdkg.DKGService_ServiceDesc,
}

// TestOperatorBrontideMatchesSoToSoServices keeps the AuthOperatorBrontide label consistent with soToSoServiceDescs in
// both directions: every method on one of those services must claim it, and no method outside them may. A new RPC added
// to SparkInternalService without an AuthOperatorBrontide entry, or an AuthOperatorBrontide entry on a service not in the
// list, both fail here. That the list itself still matches what bin/operator registers is the operator-side test's job.
func TestOperatorBrontideMatchesSoToSoServices(t *testing.T) {
	soToSo := map[string]struct{}{}
	for _, sd := range soToSoServiceDescs {
		for _, m := range fullMethodsFromServiceDesc(sd) {
			soToSo[m] = struct{}{}
		}
	}

	var missingLabel, unexpectedLabel []string
	for _, m := range RegisteredMethods() {
		p, ok := LookUp(m)
		require.True(t, ok)
		_, isSOToSO := soToSo[m]
		switch {
		case isSOToSO && p.AuthMode != AuthOperatorBrontide:
			missingLabel = append(missingLabel, m)
		case !isSOToSO && p.AuthMode == AuthOperatorBrontide:
			unexpectedLabel = append(unexpectedLabel, m)
		}
	}
	slices.Sort(missingLabel)
	slices.Sort(unexpectedLabel)

	require.Empty(t, missingLabel, "methods on SO-to-SO services must be AuthOperatorBrontide: %v", missingLabel)
	require.Empty(t, unexpectedLabel,
		"AuthOperatorBrontide is only valid for services hosted on the internal brontide listener; these aren't: %v", unexpectedLabel)
}

// TestOperatorBrontideMethodsAreInternalOnly pins the IP allowlist fallback in place. The SO-to-SO services are still
// registered on the public listener, where the IP gate is the only caller check, so brontide methods must stay
// InternalOnly until that registration is removed.
func TestOperatorBrontideMethodsAreInternalOnly(t *testing.T) {
	for _, m := range RegisteredMethods() {
		if p, _ := LookUp(m); p.AuthMode == AuthOperatorBrontide {
			assert.True(t, p.InternalOnly, "%s is AuthOperatorBrontide but not InternalOnly", m)
		}
	}
}

func TestUnknownMethodFailsClosed(t *testing.T) {
	_, ok := LookUp("/never.Registered/Method")
	assert.False(t, ok)
	assert.True(t, RequiresSessionToken("/never.Registered/Method"), "unknown methods must require authn")
	assert.False(t, IsInternalOnly("/never.Registered/Method"))
}
