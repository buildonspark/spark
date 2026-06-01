package authn

import (
	"testing"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
)

func TestDefaultUnauthenticatedConfigIncludesDirectGossip(t *testing.T) {
	if !DefaultUnauthenticatedConfig().IsUnauthenticated(pbgossip.GossipService_Gossip_FullMethodName) {
		t.Fatalf("direct gossip must remain paired with service authz when it bypasses user authn")
	}
}
