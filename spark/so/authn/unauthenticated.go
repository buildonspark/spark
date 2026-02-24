package authn

import (
	"strings"

	pbauthn "github.com/lightsparkdev/spark/proto/spark_authn"
)

// UnauthenticatedConfig defines which gRPC methods bypass authentication.
type UnauthenticatedConfig struct {
	// Methods contains the full method names of gRPC methods that do not require
	// user authentication.
	Methods map[string]struct{}
	// ServicePrefixes contains service prefixes for any services that do not require user
	// authentication.
	ServicePrefixes []string
}

// IsUnauthenticated returns true if the given gRPC method does not require
// authentication.
func (c UnauthenticatedConfig) IsUnauthenticated(fullMethod string) bool {
	if _, ok := c.Methods[fullMethod]; ok {
		return true
	}
	for _, prefix := range c.ServicePrefixes {
		if strings.HasPrefix(fullMethod, prefix) {
			return true
		}
	}
	return false
}

func baseUnauthenticatedMethods() map[string]struct{} {
	return map[string]struct{}{
		pbauthn.SparkAuthnService_GetChallenge_FullMethodName:     {},
		pbauthn.SparkAuthnService_VerifyChallenge_FullMethodName:  {},
		"/spark.SparkService/query_nodes":                         {},
		"/spark.SparkService/query_pending_transfers":             {},
		"/spark.SparkService/query_all_transfers":                 {},
		"/spark.SparkService/query_unused_deposit_addresses":      {},
		"/spark.SparkService/query_static_deposit_addresses":      {},
		"/spark.SparkService/query_balance":                       {},
		"/spark.SparkService/get_signing_operator_list":           {},
		"/spark.SparkService/query_spark_invoices":                {},
		"/spark.SparkService/get_utxos_for_address":               {},
		"/spark_token.SparkTokenService/query_token_metadata":     {},
		"/spark_token.SparkTokenService/query_token_outputs":      {},
		"/spark_token.SparkTokenService/query_token_transactions": {},
	}
}

func baseServicePrefixes() []string {
	return []string{
		"/dkg.DKGService/",
		"/gossip.GossipService/",
		"/grpc.health.v1.Health/",
		"/mock.MockService/",
		"/spark_internal.SparkInternalService/",
		"/spark_token.SparkTokenInternalService/",
	}
}
