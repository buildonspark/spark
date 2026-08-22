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
	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
	pbtoken "github.com/lightsparkdev/spark/proto/spark_token"
	pbtokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
)

// AuthMode describes how the authn interceptor treats a method.
type AuthMode int

const (
	// AuthSession requires a valid session token.
	AuthSession AuthMode = iota
	// AuthAnonymous performs no caller authentication at all. Use only for public discovery RPCs, or for reads that
	// either return public data or apply wallet-privacy filtering inside the handler.
	AuthAnonymous
	// AuthPartnerBasic means the method is authenticated, but via HTTP Basic Auth rather than a session token. The
	// authn interceptor does not require a session token (so the "Basic …" credential passes through); the credential
	// itself is verified downstream by partner.BasicAuthInterceptor against partner_keys.basic_auth_secret_hash.
	//
	// IMPORTANT: BasicAuthInterceptor is on the UNARY chain only. A *streaming* AuthPartnerBasic method would skip
	// session auth yet never have its Basic credential verified.
	AuthPartnerBasic
	// AuthOperatorBrontide means the caller is another SO, authenticated at the transport layer: the Noise_XK handshake
	// proves possession of an identity private key belonging to the operator set. No session token is involved, and the
	// authz interceptor admits the call on the strength of the resolved brontide.PeerOperator.
	//
	// IMPORTANT: for now this only holds for calls that arrive on the internal listener. The same services are still
	// registered on the public listener, where InternalOnly's IP gate is the only check. Once every peer client dials
	// brontide and these services come off the public listener, the IP fallback (and InternalOnly on these methods) can
	// go. The readiness signal is internal_authz_decisions_total{path="vpc-ip"} dropping to zero for these methods.
	AuthOperatorBrontide
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

// RequiresSessionToken reports whether the authn interceptor should require session-token enforcement for the method.
// Only AuthSession uses session tokens; the other modes are either anonymous or authenticated by a different mechanism
// (Basic Auth downstream, or the brontide handshake at the transport layer). Stated as an allowlist so unregistered
// methods and any future AuthMode fail closed and require a session.
func RequiresSessionToken(fullMethod string) bool {
	p, ok := LookUp(fullMethod)
	if !ok {
		return true
	}
	return p.AuthMode == AuthSession
}

// IsInternalOnly reports whether the authz interceptor should gate the method on caller provenance. The interceptor
// admits either a brontide-authenticated operator or a VPC-internal / allowlisted peer IP, so for AuthOperatorBrontide
// methods this is the fallback gate rather than the primary one.
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
	register(sparkPartnerServicePolicies())
}

func sparkAuthnPolicies() map[string]Policy {
	return map[string]Policy{
		pbauthn.SparkAuthnService_GetChallenge_FullMethodName:    {AuthMode: AuthAnonymous},
		pbauthn.SparkAuthnService_VerifyChallenge_FullMethodName: {AuthMode: AuthAnonymous},
	}
}

// sparkPartnerServicePolicies covers the external partner-facing read API. It's authenticated via HTTP Basic
// Auth (AuthPartnerBasic), enforced by partner.BasicAuthInterceptor against partner_keys.basic_auth_secret_hash
// rather than a session token. It is not InternalOnly: partners reach it from outside the VPC.
func sparkPartnerServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbpartner.SparkPartnerService_QuerySparkTransactionVolumes_FullMethodName: {AuthMode: AuthPartnerBasic},
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
		pbspark.SparkService_QueryPendingTransfers_FullMethodName:               {AuthMode: AuthAnonymous},
		pbspark.SparkService_QueryAllTransfers_FullMethodName:                   {AuthMode: AuthAnonymous},
		pbspark.SparkService_QueryTransfersById_FullMethodName:                  {AuthMode: AuthAnonymous},
		pbspark.SparkService_ClaimTransferTweakKeys_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_StorePreimageShare_FullMethodName:                  {AuthMode: AuthSession},
		pbspark.SparkService_StorePreimageShareV2_FullMethodName:                {AuthMode: AuthSession},
		pbspark.SparkService_GetSigningCommitments_FullMethodName:               {AuthMode: AuthSession},
		pbspark.SparkService_ProvidePreimage_FullMethodName:                     {AuthMode: AuthSession},
		pbspark.SparkService_QueryPreimage_FullMethodName:                       {AuthMode: AuthSession},
		pbspark.SparkService_QueryHtlc_FullMethodName:                           {AuthMode: AuthSession},
		pbspark.SparkService_RenewLeaf_FullMethodName:                           {AuthMode: AuthSession},
		pbspark.SparkService_GetSigningOperatorList_FullMethodName:              {AuthMode: AuthAnonymous},
		pbspark.SparkService_QueryNodes_FullMethodName:                          {AuthMode: AuthAnonymous},
		pbspark.SparkService_QueryBalance_FullMethodName:                        {AuthMode: AuthAnonymous},
		pbspark.SparkService_QueryUnusedDepositAddresses_FullMethodName:         {AuthMode: AuthAnonymous},
		pbspark.SparkService_QueryStaticDepositAddresses_FullMethodName:         {AuthMode: AuthAnonymous},
		pbspark.SparkService_SubscribeToEvents_FullMethodName:                   {AuthMode: AuthSession},
		pbspark.SparkService_InitiateStaticDepositUtxoRefund_FullMethodName:     {AuthMode: AuthSession},
		pbspark.SparkService_ExitSingleNodeTrees_FullMethodName:                 {AuthMode: AuthSession},
		pbspark.SparkService_RecoverWatchtowerExitedLeaf_FullMethodName:         {AuthMode: AuthSession},
		pbspark.SparkService_CooperativeExitV2_FullMethodName:                   {AuthMode: AuthSession},
		pbspark.SparkService_ClaimTransferSignRefundsV2_FullMethodName:          {AuthMode: AuthSession},
		pbspark.SparkService_FinalizeNodeSignaturesV2_FullMethodName:            {AuthMode: AuthSession},
		pbspark.SparkService_InitiatePreimageSwapV2_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_InitiatePreimageSwapV3_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_InitiatePreimageSwapV4_FullMethodName:              {AuthMode: AuthSession},
		pbspark.SparkService_StartTransferV2_FullMethodName:                     {AuthMode: AuthSession},
		pbspark.SparkService_StartTransferV3_FullMethodName:                     {AuthMode: AuthSession},
		pbspark.SparkService_StartTransferMpc_FullMethodName:                    {AuthMode: AuthSession},
		pbspark.SparkService_ClaimTransfer_FullMethodName:                       {AuthMode: AuthSession},
		pbspark.SparkService_GetUtxosForAddress_FullMethodName:                  {AuthMode: AuthAnonymous},
		pbspark.SparkService_GetUtxosForIdentity_FullMethodName:                 {AuthMode: AuthAnonymous},
		pbspark.SparkService_QuerySparkInvoices_FullMethodName:                  {AuthMode: AuthAnonymous},
		pbspark.SparkService_InitiateSwapPrimaryTransfer_FullMethodName:         {AuthMode: AuthSession},
		pbspark.SparkService_UpdateWalletSetting_FullMethodName:                 {AuthMode: AuthSession},
		pbspark.SparkService_QueryWalletSetting_FullMethodName:                  {AuthMode: AuthSession},
		pbspark.SparkService_CreateDelegationGrant_FullMethodName:               {AuthMode: AuthSession},
		pbspark.SparkService_RevokeDelegationGrant_FullMethodName:               {AuthMode: AuthSession},
		pbspark.SparkService_QueryDelegationGrants_FullMethodName:               {AuthMode: AuthSession},
		pbspark.SparkService_InstallLeafDecompositions_FullMethodName:           {AuthMode: AuthSession},
		pbspark.SparkService_AddDelegationSpender_FullMethodName:                {AuthMode: AuthSession},
		pbspark.SparkService_RevokeDelegationSpender_FullMethodName:             {AuthMode: AuthSession},
	}
}

// sparkInternalServicePolicies handle SO-to-SO coordination.
func sparkInternalServicePolicies() map[string]Policy {
	operatorInternal := Policy{AuthMode: AuthOperatorBrontide, InternalOnly: true}
	return map[string]Policy{
		pbinternal.SparkInternalService_MarkKeysharesAsUsed_FullMethodName:                operatorInternal,
		pbinternal.SparkInternalService_MarkKeyshareForDepositAddress_FullMethodName:      operatorInternal,
		pbinternal.SparkInternalService_ReserveEntityDkgKey_FullMethodName:                operatorInternal,
		pbinternal.SparkInternalService_FinalizeTreeCreation_FullMethodName:               operatorInternal,
		pbinternal.SparkInternalService_FrostRound1_FullMethodName:                        operatorInternal,
		pbinternal.SparkInternalService_FrostRound2_FullMethodName:                        operatorInternal,
		pbinternal.SparkInternalService_FinalizeTransfer_FullMethodName:                   operatorInternal,
		pbinternal.SparkInternalService_FinalizeRefreshTimelock_FullMethodName:            operatorInternal,
		pbinternal.SparkInternalService_FinalizeExtendLeaf_FullMethodName:                 operatorInternal,
		pbinternal.SparkInternalService_FinalizeRenewRefundTimelock_FullMethodName:        operatorInternal,
		pbinternal.SparkInternalService_FinalizeRenewNodeTimelock_FullMethodName:          operatorInternal,
		pbinternal.SparkInternalService_NodeAvailableForRenew_FullMethodName:              operatorInternal,
		pbinternal.SparkInternalService_InitiatePreimageSwap_FullMethodName:               operatorInternal,
		pbinternal.SparkInternalService_InitiatePreimageSwapV2_FullMethodName:             operatorInternal,
		pbinternal.SparkInternalService_UpdatePreimageRequest_FullMethodName:              operatorInternal,
		pbinternal.SparkInternalService_StorePreimageShare_FullMethodName:                 operatorInternal,
		pbinternal.SparkInternalService_PrepareTreeAddress_FullMethodName:                 operatorInternal,
		pbinternal.SparkInternalService_InitiateTransfer_FullMethodName:                   operatorInternal,
		pbinternal.SparkInternalService_InitiateTransferV2_FullMethodName:                 operatorInternal,
		pbinternal.SparkInternalService_DeliverSenderKeyTweak_FullMethodName:              operatorInternal,
		pbinternal.SparkInternalService_InitiateCooperativeExit_FullMethodName:            operatorInternal,
		pbinternal.SparkInternalService_InitiateSettleReceiverKeyTweak_FullMethodName:     operatorInternal,
		pbinternal.SparkInternalService_SettleReceiverKeyTweak_FullMethodName:             operatorInternal,
		pbinternal.SparkInternalService_SettleSenderKeyTweak_FullMethodName:               operatorInternal,
		pbinternal.SparkInternalService_CreateStaticDepositUtxoSwap_FullMethodName:        operatorInternal,
		pbinternal.SparkInternalService_CreateStaticDepositUtxoRefund_FullMethodName:      operatorInternal,
		pbinternal.SparkInternalService_CreateInstantStaticDepositUtxoSwap_FullMethodName: operatorInternal,
		pbinternal.SparkInternalService_SaveUtxoForInstantStaticDeposit_FullMethodName:    operatorInternal,
		pbinternal.SparkInternalService_LinkUtxoSwapTransfer_FullMethodName:               operatorInternal,
		pbinternal.SparkInternalService_RollbackUtxoSwap_FullMethodName:                   operatorInternal,
		pbinternal.SparkInternalService_RollbackInstantUtxoSwap_FullMethodName:            operatorInternal,
		pbinternal.SparkInternalService_UtxoSwapCompleted_FullMethodName:                  operatorInternal,
		pbinternal.SparkInternalService_FixKeyshare_FullMethodName:                        operatorInternal,
		pbinternal.SparkInternalService_FixKeyshareRound1_FullMethodName:                  operatorInternal,
		pbinternal.SparkInternalService_FixKeyshareRound2_FullMethodName:                  operatorInternal,
		pbinternal.SparkInternalService_GetTransfers_FullMethodName:                       operatorInternal,
		pbinternal.SparkInternalService_GenerateStaticDepositAddressProofs_FullMethodName: operatorInternal,
		pbinternal.SparkInternalService_SyncNode_FullMethodName:                           operatorInternal,
		pbinternal.SparkInternalService_QueryNodes_FullMethodName:                         operatorInternal,
		pbinternal.SparkInternalService_ConsensusPrepare_FullMethodName:                   operatorInternal,
		pbinternal.SparkInternalService_ConsensusQueryOutcome_FullMethodName:              operatorInternal,
	}
}

func sparkTokenServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbtoken.SparkTokenService_StartTransaction_FullMethodName:       {AuthMode: AuthSession},
		pbtoken.SparkTokenService_CommitTransaction_FullMethodName:      {AuthMode: AuthSession},
		pbtoken.SparkTokenService_QueryTokenMetadata_FullMethodName:     {AuthMode: AuthAnonymous},
		pbtoken.SparkTokenService_QueryTokenTransactions_FullMethodName: {AuthMode: AuthAnonymous},
		pbtoken.SparkTokenService_QueryTokenOutputs_FullMethodName:      {AuthMode: AuthAnonymous},
		pbtoken.SparkTokenService_FreezeTokens_FullMethodName:           {AuthMode: AuthSession},
		pbtoken.SparkTokenService_BroadcastTransaction_FullMethodName:   {AuthMode: AuthSession},
		pbtoken.SparkTokenService_CreateTokenAllowance_FullMethodName:   {AuthMode: AuthSession},
		pbtoken.SparkTokenService_RevokeTokenAllowance_FullMethodName:   {AuthMode: AuthSession},
		pbtoken.SparkTokenService_QueryTokenAllowances_FullMethodName:   {AuthMode: AuthSession},
	}
}

func sparkTokenInternalServicePolicies() map[string]Policy {
	operatorInternal := Policy{AuthMode: AuthOperatorBrontide, InternalOnly: true}
	return map[string]Policy{
		pbtokeninternal.SparkTokenInternalService_PrepareTransaction_FullMethodName:                   operatorInternal,
		pbtokeninternal.SparkTokenInternalService_SignTokenTransactionFromCoordination_FullMethodName: operatorInternal,
		pbtokeninternal.SparkTokenInternalService_ExchangeRevocationSecretsShares_FullMethodName:      operatorInternal,
		pbtokeninternal.SparkTokenInternalService_SignTokenTransaction_FullMethodName:                 operatorInternal,
		pbtokeninternal.SparkTokenInternalService_InternalFreezeTokens_FullMethodName:                 operatorInternal,
	}
}

func dkgServicePolicies() map[string]Policy {
	operatorInternal := Policy{AuthMode: AuthOperatorBrontide, InternalOnly: true}
	return map[string]Policy{
		pbdkg.DKGService_StartDkg_FullMethodName:          operatorInternal,
		pbdkg.DKGService_InitiateDkg_FullMethodName:       operatorInternal,
		pbdkg.DKGService_Round1Packages_FullMethodName:    operatorInternal,
		pbdkg.DKGService_Round1Signature_FullMethodName:   operatorInternal,
		pbdkg.DKGService_Round2Packages_FullMethodName:    operatorInternal,
		pbdkg.DKGService_RoundConfirmation_FullMethodName: operatorInternal,
	}
}

func gossipServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbgossip.GossipService_Gossip_FullMethodName: {AuthMode: AuthOperatorBrontide, InternalOnly: true},
	}
}

// mockServicePolicies are local-only test helpers. Registered only when running locally, they bypass session auth but are
// NOT IP-protected (so they're reachable from a developer's loopback). Prod binaries don't register these methods at all.
func mockServicePolicies() map[string]Policy {
	return map[string]Policy{
		pbmock.MockService_CleanUpPreimageShare_FullMethodName: {AuthMode: AuthAnonymous},
		pbmock.MockService_UpdateNodesStatus_FullMethodName:    {AuthMode: AuthAnonymous},
		pbmock.MockService_TriggerTask_FullMethodName:          {AuthMode: AuthAnonymous},
		pbmock.MockService_QueryPreimageShare_FullMethodName:   {AuthMode: AuthAnonymous},
		pbmock.MockService_ModifyNodeTimelock_FullMethodName:   {AuthMode: AuthAnonymous},
	}
}

// healthServicePolicies are standard grpc-health probes used by Kubernetes and load balancers. The full method names
// are stable parts of the gRPC health protocol, so referencing them as string literals is safe.
func healthServicePolicies() map[string]Policy {
	return map[string]Policy{
		"/grpc.health.v1.Health/Check": {AuthMode: AuthAnonymous},
		"/grpc.health.v1.Health/List":  {AuthMode: AuthAnonymous},
		"/grpc.health.v1.Health/Watch": {AuthMode: AuthAnonymous},
	}
}
