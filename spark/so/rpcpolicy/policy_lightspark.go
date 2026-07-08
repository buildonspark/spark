//go:build lightspark

package rpcpolicy

import (
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
)

// SparkSspInternalService is the Lightspark-only SSP coordination surface.
// Every method is IP-protected; subsets either accept anonymous callers (legacy "ops" RPCs) or require a session token
// (newer flows that go through the same sspcore client used by external partners).
func init() {
	register(sparkSspInternalServicePolicies())
}

func sparkSspInternalServicePolicies() map[string]Policy {
	unauthInternal := Policy{AuthMode: AuthUnauthenticated, InternalOnly: true}
	sessionInternal := Policy{AuthMode: AuthSession, InternalOnly: true}
	return map[string]Policy{
		// Anonymous, IP-restricted ops RPCs.
		pbssp.SparkSspInternalService_QueryLostNodes_FullMethodName:              unauthInternal,
		pbssp.SparkSspInternalService_GetStuckTransfers_FullMethodName:           unauthInternal,
		pbssp.SparkSspInternalService_QueryStuckTransfer_FullMethodName:          unauthInternal,
		pbssp.SparkSspInternalService_CancelStuckTransfer_FullMethodName:         unauthInternal,
		pbssp.SparkSspInternalService_ReturnStuckTransfer_FullMethodName:         unauthInternal,
		pbssp.SparkSspInternalService_GetStuckLightningPayments_FullMethodName:   unauthInternal,
		pbssp.SparkSspInternalService_ReturnStuckTransfers_FullMethodName:        unauthInternal,
		pbssp.SparkSspInternalService_QueryNodeTransferHistory_FullMethodName:    unauthInternal,
		pbssp.SparkSspInternalService_ApplySenderKeyTweaks_FullMethodName:        unauthInternal,
		pbssp.SparkSspInternalService_FixKeyshare_FullMethodName:                 unauthInternal,
		pbssp.SparkSspInternalService_SyncTransfer_FullMethodName:                unauthInternal,
		pbssp.SparkSspInternalService_SyncExitedTrees_FullMethodName:             unauthInternal,
		pbssp.SparkSspInternalService_DepositCleanup_FullMethodName:              unauthInternal,
		pbssp.SparkSspInternalService_QueryNodes_FullMethodName:                  unauthInternal,
		pbssp.SparkSspInternalService_SyncTreeNodes_FullMethodName:               unauthInternal,
		pbssp.SparkSspInternalService_SyncTreeNodesCoordinator_FullMethodName:    unauthInternal,
		pbssp.SparkSspInternalService_InitiateCounterTransfer_FullMethodName:     unauthInternal,
		pbssp.SparkSspInternalService_QueryTransfers_FullMethodName:              unauthInternal,
		pbssp.SparkSspInternalService_QueryStaticDepositAddresses_FullMethodName: unauthInternal,

		// Session-authenticated, IP-restricted SSP flows.
		pbssp.SparkSspInternalService_QueryLightningSwapTransfer_FullMethodName:          sessionInternal,
		pbssp.SparkSspInternalService_ExitTrees_FullMethodName:                           sessionInternal,
		pbssp.SparkSspInternalService_TweakKeysForCoopExit_FullMethodName:                sessionInternal,
		pbssp.SparkSspInternalService_InitiateStaticDepositUtxoSwap_FullMethodName:       sessionInternal,
		pbssp.SparkSspInternalService_ReserveInstantStaticDepositUtxoSwap_FullMethodName: sessionInternal,
		pbssp.SparkSspInternalService_ClaimInstantStaticDepositUtxoSwap_FullMethodName:   sessionInternal,
		pbssp.SparkSspInternalService_PrepareTreeAddress_FullMethodName:                  sessionInternal,
		pbssp.SparkSspInternalService_CreateTree_FullMethodName:                          sessionInternal,
		pbssp.SparkSspInternalService_CancelTransfer_FullMethodName:                      sessionInternal,
		pbssp.SparkSspInternalService_GenerateDepositAddress_FullMethodName:              sessionInternal,
		pbssp.SparkSspInternalService_ResolveTreeLookup_FullMethodName:                   sessionInternal,
		pbssp.SparkSspInternalService_GetTreeSnapshot_FullMethodName:                     sessionInternal,
		pbssp.SparkSspInternalService_GetNodeChildren_FullMethodName:                     sessionInternal,
		pbssp.SparkSspInternalService_GetTreeTransfers_FullMethodName:                    sessionInternal,
		pbssp.SparkSspInternalService_GetNodeDetails_FullMethodName:                      sessionInternal,
	}
}
