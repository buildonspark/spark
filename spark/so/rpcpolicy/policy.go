// Package rpcpolicy declares the authentication and authorization policy for every gRPC method registered on the
// operator server.
//
// Both the authn and authz interceptors consult it, and a completeness check (TestPoliciesAreComplete + a startup guard
// in bin/operator) ensures every registered method has an explicit entry.
//
// For now, handler-level identity binding (authz.EnforceSessionIdentityPublicKeyMatches) and wallet read-access
// filtering (HasReadAccessToWallet) remain in their handlers; later on, the plan is to extend Policy with declarative
// annotations for those checks.
package rpcpolicy

import (
	"maps"
	"slices"

	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbmock "github.com/lightsparkdev/spark/proto/mock"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbauthn "github.com/lightsparkdev/spark/proto/spark_authn"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	pbtoken "github.com/lightsparkdev/spark/proto/spark_token"
	pbtokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
)

// AuthMode describes how the authn interceptor treats a method.
type AuthMode int

const (
	// AuthSession requires a valid session token.
	AuthSession AuthMode = iota
	// AuthUnauthenticated skips session-token verification. Use only for public discovery RPCs or for internal RPCs
	// that rely on IP allowlisting instead of session auth.
	AuthUnauthenticated
)

// Policy is the per-method declarative policy.
type Policy struct {
	// AuthMode controls session-token enforcement in the authn interceptor.
	AuthMode AuthMode
	// InternalOnly, when true, instructs the authz interceptor to require a VPC-internal or allowlisted peer IP.
	InternalOnly bool
}

// policies holds the canonical table. It's built at init time from the per-service contributions in this file
// (and policy_lightspark.go for build-tagged additions).
var policies = map[string]Policy{}

func register(table map[string]Policy) {
	for method, policy := range table {
		if _, dup := policies[method]; dup {
			panic("rpcpolicy: duplicate policy for " + method)
		}
		policies[method] = policy
	}
}

// LookUp returns the policy for the given full gRPC method name (e.g. "/spark.SparkService/start_transfer_v2") and whether one is registered.
func LookUp(fullMethod string) (Policy, bool) {
	p, ok := policies[fullMethod]
	return p, ok
}

// IsAuthenticated reports whether the authn interceptor should require session-token enforcement for the method.
// Unknown methods default to requiring a session, and fail closed if someone forgets to register a new RPC.
func IsAuthenticated(fullMethod string) bool {
	p, ok := LookUp(fullMethod)
	return !ok || p.AuthMode != AuthUnauthenticated
}

// IsInternalOnly reports whether the authz interceptor should require a VPC-internal or allowlisted peer IP for the
// method. Unknown methods default to false; the startup guard rejects them anyway so this default isn't possible in prod.
func IsInternalOnly(fullMethod string) bool {
	p, ok := LookUp(fullMethod)
	return ok && p.InternalOnly
}

// RegisteredMethods returns a copy of the registered method names. Intended for completeness tests and the startup guard.
func RegisteredMethods() []string {
	return slices.Collect(maps.Keys(policies))
}

func init() {
	register(sparkAuthnPolicies())
	register(sparkServicePolicies())
	register(sparkInternalServicePolicies())
	register(sparkTokenServicePolicies())
	register(sparkTokenInternalServicePolicies())
	register(dkgServicePolicies())
	register(gossipServicePolicies())
	register(mockServicePolicies())
	register(healthServicePolicies())
}

func sparkAuthnPolicies() map[string]Policy {
	return map[string]Policy{
		pbauthn.SparkAuthnService_GetChallenge_FullMethodName:    {AuthMode: AuthUnauthenticated},
		pbauthn.SparkAuthnService_VerifyChallenge_FullMethodName: {AuthMode: AuthUnauthenticated},
	}
}

// sparkServicePolicies covers the user-facing public RPCs. Most require a session; the read-only query RPCs intentionally
// accept anonymous callers because they either return public data or apply wallet-privacy filtering inside the handler
// (e.g. HasReadAccessToWallet in tree_query_handler.go).
func sparkServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbspark.SparkService_GenerateDepositAddress_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_GenerateStaticDepositAddress_FullMethodName:        {AuthMode: AuthSession},
		pbspark.SparkService_RotateStaticDepositAddress_FullMethodName:          {AuthMode: AuthSession},
		pbspark.SparkService_StartDepositTreeCreation_FullMethodName:            {AuthMode: AuthSession},
		pbspark.SparkService_FinalizeDepositTreeCreation_FullMethodName:         {AuthMode: AuthSession},
		pbspark.SparkService_FinalizeTransferWithTransferPackage_FullMethodName: {AuthMode: AuthSession},
		pbspark.SparkService_QueryPendingTransfers_FullMethodName:               {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_QueryAllTransfers_FullMethodName:                   {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_ClaimTransferTweakKeys_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_StorePreimageShare_FullMethodName:                  {AuthMode: AuthSession},
		pbspark.SparkService_StorePreimageShareV2_FullMethodName:                {AuthMode: AuthSession},
		pbspark.SparkService_GetSigningCommitments_FullMethodName:               {AuthMode: AuthSession},
		pbspark.SparkService_ProvidePreimage_FullMethodName:                     {AuthMode: AuthSession},
		pbspark.SparkService_QueryPreimage_FullMethodName:                       {AuthMode: AuthSession},
		pbspark.SparkService_QueryHtlc_FullMethodName:                           {AuthMode: AuthSession},
		pbspark.SparkService_RenewLeaf_FullMethodName:                           {AuthMode: AuthSession},
		pbspark.SparkService_GetSigningOperatorList_FullMethodName:              {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_QueryNodes_FullMethodName:                          {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_QueryBalance_FullMethodName:                        {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_QueryUserSignedRefunds_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_QueryUnusedDepositAddresses_FullMethodName:         {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_QueryStaticDepositAddresses_FullMethodName:         {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_SubscribeToEvents_FullMethodName:                   {AuthMode: AuthSession},
		pbspark.SparkService_InitiateStaticDepositUtxoRefund_FullMethodName:     {AuthMode: AuthSession},
		pbspark.SparkService_ExitSingleNodeTrees_FullMethodName:                 {AuthMode: AuthSession},
		pbspark.SparkService_CooperativeExitV2_FullMethodName:                   {AuthMode: AuthSession},
		pbspark.SparkService_ClaimTransferSignRefundsV2_FullMethodName:          {AuthMode: AuthSession},
		pbspark.SparkService_FinalizeNodeSignaturesV2_FullMethodName:            {AuthMode: AuthSession},
		pbspark.SparkService_InitiatePreimageSwapV2_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_InitiatePreimageSwapV3_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_StartLeafSwapV2_FullMethodName:                     {AuthMode: AuthSession},
		pbspark.SparkService_StartTransferV2_FullMethodName:                     {AuthMode: AuthSession},
		pbspark.SparkService_StartTransferV3_FullMethodName:                     {AuthMode: AuthSession},
		pbspark.SparkService_ClaimTransfer_FullMethodName:                       {AuthMode: AuthSession},
		pbspark.SparkService_GetUtxosForAddress_FullMethodName:                  {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_GetUtxosForIdentity_FullMethodName:                 {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_QuerySparkInvoices_FullMethodName:                  {AuthMode: AuthUnauthenticated},
		pbspark.SparkService_InitiateSwapPrimaryTransfer_FullMethodName:         {AuthMode: AuthSession},
		pbspark.SparkService_UpdateWalletSetting_FullMethodName:                 {AuthMode: AuthSession},
		pbspark.SparkService_QueryWalletSetting_FullMethodName:                  {AuthMode: AuthSession},
	}
}

// sparkInternalServicePolicies handle SO-to-SO coordination. Every method is unauthenticated at the session layer and
// protected by VPC IP allowlisting.
func sparkInternalServicePolicies() map[string]Policy {
	unauthInternal := Policy{AuthMode: AuthUnauthenticated, InternalOnly: true}
	return map[string]Policy{
		pbinternal.SparkInternalService_MarkKeysharesAsUsed_FullMethodName:                unauthInternal,
		pbinternal.SparkInternalService_MarkKeyshareForDepositAddress_FullMethodName:      unauthInternal,
		pbinternal.SparkInternalService_ReserveEntityDkgKey_FullMethodName:                unauthInternal,
		pbinternal.SparkInternalService_FinalizeTreeCreation_FullMethodName:               unauthInternal,
		pbinternal.SparkInternalService_FrostRound1_FullMethodName:                        unauthInternal,
		pbinternal.SparkInternalService_FrostRound2_FullMethodName:                        unauthInternal,
		pbinternal.SparkInternalService_FinalizeTransfer_FullMethodName:                   unauthInternal,
		pbinternal.SparkInternalService_FinalizeRefreshTimelock_FullMethodName:            unauthInternal,
		pbinternal.SparkInternalService_FinalizeExtendLeaf_FullMethodName:                 unauthInternal,
		pbinternal.SparkInternalService_FinalizeRenewRefundTimelock_FullMethodName:        unauthInternal,
		pbinternal.SparkInternalService_FinalizeRenewNodeTimelock_FullMethodName:          unauthInternal,
		pbinternal.SparkInternalService_NodeAvailableForRenew_FullMethodName:              unauthInternal,
		pbinternal.SparkInternalService_InitiatePreimageSwap_FullMethodName:               unauthInternal,
		pbinternal.SparkInternalService_InitiatePreimageSwapV2_FullMethodName:             unauthInternal,
		pbinternal.SparkInternalService_UpdatePreimageRequest_FullMethodName:              unauthInternal,
		pbinternal.SparkInternalService_StorePreimageShare_FullMethodName:                 unauthInternal,
		pbinternal.SparkInternalService_PrepareTreeAddress_FullMethodName:                 unauthInternal,
		pbinternal.SparkInternalService_InitiateTransfer_FullMethodName:                   unauthInternal,
		pbinternal.SparkInternalService_InitiateTransferV2_FullMethodName:                 unauthInternal,
		pbinternal.SparkInternalService_DeliverSenderKeyTweak_FullMethodName:              unauthInternal,
		pbinternal.SparkInternalService_InitiateCooperativeExit_FullMethodName:            unauthInternal,
		pbinternal.SparkInternalService_InitiateSettleReceiverKeyTweak_FullMethodName:     unauthInternal,
		pbinternal.SparkInternalService_SettleReceiverKeyTweak_FullMethodName:             unauthInternal,
		pbinternal.SparkInternalService_SettleSenderKeyTweak_FullMethodName:               unauthInternal,
		pbinternal.SparkInternalService_CreateStaticDepositUtxoSwap_FullMethodName:        unauthInternal,
		pbinternal.SparkInternalService_CreateStaticDepositUtxoRefund_FullMethodName:      unauthInternal,
		pbinternal.SparkInternalService_CreateInstantStaticDepositUtxoSwap_FullMethodName: unauthInternal,
		pbinternal.SparkInternalService_SaveUtxoForInstantStaticDeposit_FullMethodName:    unauthInternal,
		pbinternal.SparkInternalService_LinkUtxoSwapTransfer_FullMethodName:               unauthInternal,
		pbinternal.SparkInternalService_RollbackUtxoSwap_FullMethodName:                   unauthInternal,
		pbinternal.SparkInternalService_RollbackInstantUtxoSwap_FullMethodName:            unauthInternal,
		pbinternal.SparkInternalService_UtxoSwapCompleted_FullMethodName:                  unauthInternal,
		pbinternal.SparkInternalService_QueryLeafSigningPubkeys_FullMethodName:            unauthInternal,
		pbinternal.SparkInternalService_ResolveLeafInvestigation_FullMethodName:           unauthInternal,
		pbinternal.SparkInternalService_FixKeyshare_FullMethodName:                        unauthInternal,
		pbinternal.SparkInternalService_FixKeyshareRound1_FullMethodName:                  unauthInternal,
		pbinternal.SparkInternalService_FixKeyshareRound2_FullMethodName:                  unauthInternal,
		pbinternal.SparkInternalService_GetTransfers_FullMethodName:                       unauthInternal,
		pbinternal.SparkInternalService_GenerateStaticDepositAddressProofs_FullMethodName: unauthInternal,
		pbinternal.SparkInternalService_SyncNode_FullMethodName:                           unauthInternal,
		pbinternal.SparkInternalService_ConsensusPrepare_FullMethodName:                   unauthInternal,
		pbinternal.SparkInternalService_ConsensusQueryOutcome_FullMethodName:              unauthInternal,
	}
}

func sparkTokenServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbtoken.SparkTokenService_StartTransaction_FullMethodName:       {AuthMode: AuthSession},
		pbtoken.SparkTokenService_CommitTransaction_FullMethodName:      {AuthMode: AuthSession},
		pbtoken.SparkTokenService_QueryTokenMetadata_FullMethodName:     {AuthMode: AuthUnauthenticated},
		pbtoken.SparkTokenService_QueryTokenTransactions_FullMethodName: {AuthMode: AuthUnauthenticated},
		pbtoken.SparkTokenService_QueryTokenOutputs_FullMethodName:      {AuthMode: AuthUnauthenticated},
		pbtoken.SparkTokenService_FreezeTokens_FullMethodName:           {AuthMode: AuthSession},
		pbtoken.SparkTokenService_BroadcastTransaction_FullMethodName:   {AuthMode: AuthSession},
	}
}

func sparkTokenInternalServicePolicies() map[string]Policy {
	unauthInternal := Policy{AuthMode: AuthUnauthenticated, InternalOnly: true}
	return map[string]Policy{
		pbtokeninternal.SparkTokenInternalService_PrepareTransaction_FullMethodName:                   unauthInternal,
		pbtokeninternal.SparkTokenInternalService_SignTokenTransactionFromCoordination_FullMethodName: unauthInternal,
		pbtokeninternal.SparkTokenInternalService_ExchangeRevocationSecretsShares_FullMethodName:      unauthInternal,
		pbtokeninternal.SparkTokenInternalService_SignTokenTransaction_FullMethodName:                 unauthInternal,
		pbtokeninternal.SparkTokenInternalService_InternalFreezeTokens_FullMethodName:                 unauthInternal,
	}
}

func dkgServicePolicies() map[string]Policy {
	unauthInternal := Policy{AuthMode: AuthUnauthenticated, InternalOnly: true}
	return map[string]Policy{
		pbdkg.DKGService_StartDkg_FullMethodName:          unauthInternal,
		pbdkg.DKGService_InitiateDkg_FullMethodName:       unauthInternal,
		pbdkg.DKGService_Round1Packages_FullMethodName:    unauthInternal,
		pbdkg.DKGService_Round1Signature_FullMethodName:   unauthInternal,
		pbdkg.DKGService_Round2Packages_FullMethodName:    unauthInternal,
		pbdkg.DKGService_RoundConfirmation_FullMethodName: unauthInternal,
	}
}

func gossipServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbgossip.GossipService_Gossip_FullMethodName: {AuthMode: AuthUnauthenticated, InternalOnly: true},
	}
}

// mockServicePolicies are local-only test helpers. Registered only when running locally, they bypass session auth but are
// NOT IP-protected (so they're reachable from a developer's loopback). Prod binaries don't register these methods at all.
func mockServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbmock.MockService_CleanUpPreimageShare_FullMethodName: {AuthMode: AuthUnauthenticated},
		pbmock.MockService_UpdateNodesStatus_FullMethodName:    {AuthMode: AuthUnauthenticated},
		pbmock.MockService_TriggerTask_FullMethodName:          {AuthMode: AuthUnauthenticated},
		pbmock.MockService_QueryPreimageShare_FullMethodName:   {AuthMode: AuthUnauthenticated},
		pbmock.MockService_ModifyNodeTimelock_FullMethodName:   {AuthMode: AuthUnauthenticated},
	}
}

// healthServicePolicies are standard grpc-health probes used by Kubernetes and load balancers. The full method names
// are stable parts of the gRPC health protocol, so referencing them as string literals is safe.
func healthServicePolicies() map[string]Policy {
	return map[string]Policy{
		"/grpc.health.v1.Health/Check": {AuthMode: AuthUnauthenticated},
		"/grpc.health.v1.Health/List":  {AuthMode: AuthUnauthenticated},
		"/grpc.health.v1.Health/Watch": {AuthMode: AuthUnauthenticated},
	}
}
