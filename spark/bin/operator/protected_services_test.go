//go:build !lightspark

package main

import (
	"slices"
	"testing"

	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbtokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
)

func TestInternalServicesRemainAuthzProtected(t *testing.T) {
	protected := GetProtectedServices()
	for _, service := range []string{
		pbdkg.DKGService_ServiceDesc.ServiceName,
		pbgossip.GossipService_ServiceDesc.ServiceName,
		pbinternal.SparkInternalService_ServiceDesc.ServiceName,
		pbtokeninternal.SparkTokenInternalService_ServiceDesc.ServiceName,
	} {
		if !slices.Contains(protected, service) {
			t.Fatalf("expected %s to stay in GetProtectedServices", service)
		}
	}
}
