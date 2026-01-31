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

// DefaultUnauthenticatedConfig returns the production configuration.
func DefaultUnauthenticatedConfig() UnauthenticatedConfig {
	return UnauthenticatedConfig{
		Methods: map[string]struct{}{
			// Auth bootstrap endpoints
			pbauthn.SparkAuthnService_GetChallenge_FullMethodName:    {},
			pbauthn.SparkAuthnService_VerifyChallenge_FullMethodName: {},
			// SSP internal methods that don't have session checks.
			// These rely on IP-based restrictions for security.
			"/spark_ssp_internal.SparkSspInternalService/query_lost_nodes":             {},
			"/spark_ssp_internal.SparkSspInternalService/query_magic_swap_nodes":       {},
			"/spark_ssp_internal.SparkSspInternalService/get_stuck_transfers":          {},
			"/spark_ssp_internal.SparkSspInternalService/query_stuck_transfer":         {},
			"/spark_ssp_internal.SparkSspInternalService/cancel_stuck_transfer":        {},
			"/spark_ssp_internal.SparkSspInternalService/return_stuck_transfer":        {},
			"/spark_ssp_internal.SparkSspInternalService/get_stuck_lightning_payments": {},
			"/spark_ssp_internal.SparkSspInternalService/return_stuck_transfers":       {},
			"/spark_ssp_internal.SparkSspInternalService/query_node_transfer_history":  {},
			"/spark_ssp_internal.SparkSspInternalService/apply_sender_key_tweaks":      {},
			"/spark_ssp_internal.SparkSspInternalService/fix_keyshare":                 {},
			"/spark_ssp_internal.SparkSspInternalService/sync_transfer":                {},
			"/spark_ssp_internal.SparkSspInternalService/sync_exited_trees":            {},
			"/spark_ssp_internal.SparkSspInternalService/deposit_cleanup":              {},
			"/spark_ssp_internal.SparkSspInternalService/sync_tree_nodes":              {},
			"/spark_ssp_internal.SparkSspInternalService/sync_tree_nodes_coordinator":  {},
			"/spark_ssp_internal.SparkSspInternalService/initiate_counter_transfer":    {},
			"/spark_ssp_internal.SparkSspInternalService/counter_leaf_swap_v2":         {},
			"/spark_ssp_internal.SparkSspInternalService/query_transfers":              {},
			"/spark_ssp_internal.SparkSspInternalService/query_nodes":                  {},
			// Public SparkService methods that don't have session checks.
			// These rely on other authorization mechanisms (e.g., privacy settings, ownership checks).
			"/spark.SparkService/query_nodes":                    {},
			"/spark.SparkService/query_pending_transfers":        {},
			"/spark.SparkService/query_all_transfers":            {},
			"/spark.SparkService/query_unused_deposit_addresses": {},
			"/spark.SparkService/query_static_deposit_addresses": {},
			"/spark.SparkService/query_balance":                  {},
			"/spark.SparkService/get_signing_operator_list":      {},
			"/spark.SparkService/query_spark_invoices":           {},
			"/spark.SparkService/get_utxos_for_address":          {},
			// Token query methods
			"/spark_token.SparkTokenService/query_token_metadata":     {},
			"/spark_token.SparkTokenService/query_token_outputs":      {},
			"/spark_token.SparkTokenService/query_token_transactions": {},
		},
		ServicePrefixes: []string{
			"/dkg.DKGService/",
			"/gossip.GossipService/",
			"/grpc.health.v1.Health/",
			"/mock.MockService/",
			"/spark_internal.SparkInternalService/",
			"/spark_token.SparkTokenInternalService/",
		},
	}
}

// IsUnauthenticated returns true if the given gRPC method does not require
// authentication. This is used by the AuthnInterceptor to skip authentication
// for bootstrap endpoints and internal services.
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
