package knobs

import (
	"context"
	"crypto/md5"
	"fmt"
	"math/big"
	"math/rand"
	"time"

	"github.com/google/uuid"
)

const (
	KnobDatabaseStatementTimeout          = "spark.database.statement_timeout"
	KnobDatabaseLockTimeout               = "spark.database.lock_timeout"
	KnobDatabasePoolMinConns              = "spark.database.pool.min_conns"
	KnobDatabasePoolMaxConns              = "spark.database.pool.max_conns"
	KnobDatabasePoolMaxConnLifetime       = "spark.database.pool.max_conn_lifetime"
	KnobDatabasePoolMaxConnLifetimeJitter = "spark.database.pool.max_conn_lifetime_jitter"
	KnobDatabasePoolMaxConnIdleTime       = "spark.database.pool.max_conn_idle_time"
	KnobDatabasePoolHealthCheckPeriod     = "spark.database.pool.health_check_period"

	KnobRateLimitLimit              = "spark.so.ratelimit.limit"
	KnobRateLimitExcludeIps         = "spark.so.ratelimit.exclude_ips"
	KnobRateLimitExcludePubkeys     = "spark.so.ratelimit.exclude_pubkeys"
	KnobRateLimitExcludeIpsOnly     = "spark.so.ratelimit.exclude_ips_only"
	KnobRateLimitExcludePubkeysOnly = "spark.so.ratelimit.exclude_pubkeys_only"
	// Enable Memcached-backed store for rate limiter when > 0
	KnobRateLimitMemcacheEnabled      = "spark.so.ratelimit.memcache.enabled"
	KnobRateLimitMemcacheMaxIdleConns = "spark.so.ratelimit.memcache.max_idle_conns"
	KnobSoTransferLimit               = "spark.so.transfer_limit"

	// KnobMimoAuthoritativeReceiverStatusEnabled, when >0, keeps multi-receiver
	// (N>1) transfers at SENDER_KEY_TWEAKED through the claim and advances the
	// parent straight to COMPLETED only once all receivers finish. Flip on all
	// SOs together — the internal settle RPCs run on every operator.
	KnobMimoAuthoritativeReceiverStatusEnabled = "spark.so.mimo_authoritative_receiver_status_enabled"

	// Per-call sampling rate (0–100) for the "transfer query invoked" caller-attribution log.
	// Default 0 = off; bump to 100 (or a small percentage) during diagnosis.
	KnobLogTransferQueryInvocations = "spark.so.log_transfer_query_invocations"

	// Slow-call threshold in milliseconds. When > 0, "transfer query invoked"
	// invocations with elapsed >= threshold log unconditionally and carry
	// slow_query=true regardless of the sampling knob.
	KnobLogTransferQueryInvocationsSlowMs = "spark.so.log_transfer_query_invocations_slow_ms"

	KnobSoSigningCommitmentNodeLimit  = "spark.so.signing_commitments.nodes_limit"
	KnobSoSigningCommitmentCountLimit = "spark.so.signing_commitments.count_limit"

	KnobGrpcServerMethodEnabled          = "spark.so.grpc.server.method.enabled"
	KnobGrpcServerConnectionTimeout      = "spark.so.grpc.server.connection_timeout"
	KnobGrpcServerKeepaliveTime          = "spark.so.grpc.server.keepalive_time"
	KnobGrpcServerKeepaliveTimeout       = "spark.so.grpc.server.keepalive_timeout"
	KnobGrpcServerMaxConnectionAge       = "spark.so.grpc.server.max_connection_age"
	KnobGrpcServerMaxConnectionAgeGrace  = "spark.so.grpc.server.max_connection_age_grace"
	KnobGrpcServerUnaryHandlerTimeout    = "spark.so.grpc.server.unary_handler_timeout"
	KnobGrpcServerStreamHeartbeatEnabled = "spark.so.grpc.server.stream_heartbeat.enabled"

	KnobGrpcServerConcurrencyLimitLimit     = "spark.so.grpc.server.concurrency_limit.limit"
	KnobGrpcServerConcurrencyExcludeIps     = "spark.so.grpc.server.concurrency_limit.exclude_ips"
	KnobGrpcServerConcurrencyExcludePubkeys = "spark.so.grpc.server.concurrency_limit.exclude_pubkeys"

	// KnobKillSwitchWallet blocks state-mutating user-facing operations for a
	// specific wallet identity public key. Set
	// spark.so.killswitch.wallet@<identity_pubkey_hex> = 1 to freeze that wallet.
	// Default 0 (allowed). The check is per-SO; for a system-wide freeze set
	// this on every SO's knob ConfigMap. Read-only RPCs are unaffected.
	KnobKillSwitchWallet = "spark.so.killswitch.wallet"

	KnobSoMaxTransactionsPerRequest               = "spark.so.max_transactions_per_request"
	KnobSoMaxParallelFrostValidationsPerRequest   = "spark.so.max_parallel_frost_validations_per_request"
	KnobSoMaxKeysharesPerRequest                  = "spark.so.max_keyshares_per_request"
	KnobSoSigningKeyshareDualWriteSecret          = "spark.so.signing_keyshare.dual_write_secret_share"
	KnobSoBackfillSigningKeyshareSecretsEnabled   = "spark.so.signing_keyshare.backfill_secrets_enabled"
	KnobSoClearSigningKeyshareSecretSharesEnabled = "spark.so.signing_keyshare.clear_secret_shares_enabled"
	KnobGRPCClientTimeout                         = "spark.so.grpc.client.timeout"
	KnobGrpcClientPoolMinConnections              = "spark.so.grpc.client.pool.min_connections"
	KnobGrpcClientPoolMaxConnections              = "spark.so.grpc.client.pool.max_connections"
	KnobGrpcClientPoolIdleTimeoutSeconds          = "spark.so.grpc.client.pool.idle_timeout_seconds"
	KnobGrpcClientPoolMaxLifetimeSeconds          = "spark.so.grpc.client.pool.max_lifetime_seconds"
	KnobGrpcClientPoolUsersPerConnectionCap       = "spark.so.grpc.client.pool.users_per_connection_cap"
	KnobGrpcClientPoolScaleConcurrency            = "spark.so.grpc.client.pool.scale_concurrency"

	KnobSoDkgBatchSize = "spark.so.dkg.batch_size"

	// KnobFrostAggregateBatchEnabled routes multi-job FROST signature
	// aggregation through the signer's aggregate_frost_batch RPC (one round
	// trip, jobs fanned out across cores) instead of one aggregate_frost call
	// per job. Interpreted as binary (any non-zero value enables). The knob is
	// coordinator-local — it only changes how an SO talks to its own signer
	// sidecar, so it can be flipped per-SO without coordination. Default off;
	// enable only once the SO's signer runs a build that serves
	// aggregate_frost_batch, and flip back to 0 as a kill switch.
	KnobFrostAggregateBatchEnabled = "spark.so.frost.aggregate_batch_enabled"

	// KnobInternalRPCBrontideEnabled gates whether outbound SO-to-SO internal RPCs actually route over brontide
	// (TLS + Noise_XK) at dial time. Brontide must first be provisioned via the internal_grpc_port config
	// flag, which validates peer metadata and installs the client factory at startup; this knob then flips the live
	// routing without a redeploy (and serves as a runtime kill-switch back to plain TLS). Default 0 = off, so a
	// provisioned-but-not-yet-enabled operator keeps dialing plain TLS until the knob is set > 0.
	KnobInternalRPCBrontideEnabled = "spark.so.internal_rpc.brontide_enabled"

	// Task / gocron related knobs.
	KnobSoTaskEnabled = "spark.so.task.enabled"
	KnobSoTaskTimeout = "spark.so.task.timeout"

	// Watch Chain
	// Set to 0 to disable updating exiting Tree Nodes in Chain Watcher.
	// DANGEROUS: Disabling it can lead to loss of funds.
	KnobWatchChainMarkExitingNodesEnabled = "spark.so.watch_chain.mark_exiting_nodes.enabled"

	// CoopExitConfirmationThreshold is the required L1 confirmation count for
	// both the watch-chain key-tweak advance and the receiver-claim
	// finalization guard. Both call sites use this constant directly so they
	// can never disagree.
	CoopExitConfirmationThreshold = 1

	// Cooperative Exit
	// When enabled (> 0), rejects a coop-exit request unless
	// connector_tx.TxIn[0].PreviousOutPoint.Hash equals the supplied exit_txid.
	// By default (0) the check is skipped (preserving prior behavior); flip to 1
	// after observing in dry-run logs that no legitimate caller would be
	// rejected. When 0, the alibi-tx claim vulnerability is open.

	// Tokens
	KnobTokenTransactionV3Enabled = "spark.so.tokens.token_transaction_v3_enabled"
	// Enable Phase 2 of the token transaction v3 migration which combines the internal prepare and sign RPCs into a single RPC.
	// This will be flipped to true permanently (with Phase 1 and legacy handlers being cleaned up) once we are confident in the migration
	// (which means passing integration tests, load tests, and likely an incremental production rollout).
	KnobTokenTransactionV3Phase2Enabled = "spark.so.tokens.token_transaction_v3_phase2_enabled"
	// Enable the retry task for Phase 2 token transaction broadcasts.
	// When enabled, a scheduled task will retry broadcasting SIGNED token transactions to non-coordinator SOs
	// that failed during the initial fanout. This is separate from phase 2 enablement to allow independent rollout control.
	KnobTokenTransactionV3Phase2RetryEnabled = "spark.so.tokens.token_transaction_v3_phase2_retry_enabled"
	KnobAllowExtraMetadataOnMainnet          = "spark.so.tokens.allow_extra_metadata_on_mainnet"
	// Enable backfill task to update SIGNED v3 mint/create transactions to FINALIZED status.
	// When enabled, a scheduled task will find SIGNED v3 mint/create transactions where all outputs
	// are in finalized/spent states and update them to FINALIZED status.
	KnobBackfillCreateMintFinalizedStatusEnabled = "spark.so.tokens.backfill_create_mint_finalized_status_enabled"
	KnobCoordinatedFreezeEnabled                 = "spark.so.tokens.coordinated_freeze_enabled"
	// When enabled (> 0), allows multisig issuer signatures for issuer operations (mint, freeze).
	// Gated until the roles system provides proper authority management.
	KnobMultisigIssuerEnabled = "spark.so.tokens.multisig_issuer_enabled"
	// KnobTokenBroadcastAllowedPubkeys allows specific identity public keys to broadcast token
	// transactions on behalf of any identity, bypassing the sender identity check.
	// Use as a per-pubkey target: spark.so.tokens.broadcast_allowed_pubkeys@<identityPubKeyHex> = 1
	KnobTokenBroadcastAllowedPubkeys = "spark.so.tokens.broadcast_allowed_pubkeys"

	// Tokens - Killswitches
	// When enabled (> 0), enforces owner signature validation for token withdrawals.
	// By default (0), signature validation is skipped. Enable in production when SE signatures are available.
	KnobEnforceWithdrawalSignatureValidation = "spark.so.tokens.enforce_withdrawal_signature_validation"
	// Enable justice transaction broadcasting for invalid token withdrawals.
	// When enabled (> 0), the SO will broadcast justice transactions to reclaim funds
	// from invalid withdrawals where the revocation secret is available.
	KnobEnableJusticeTransactions = "spark.so.tokens.enable_justice_transactions"
	// When enabled (> 0), allows token L1 exit withdrawals to be processed.
	// By default (0), all token L1 exit withdrawals observed on Bitcoin L1 are skipped and not
	// recorded. Flip to 1 per-environment when the L1 exit flow is ready.
	KnobTokenL1ExitWithdrawalsEnabled = "spark.so.tokens.l1_exit_withdrawals_enabled"

	// When enabled (> 0), emits per-participant TokenTransactionEvent
	// notifications on the unified SubscribeToEvents stream.
	KnobTokenTxEventsEnabled = "spark.so.tokens.tx_events_enabled"

	// Number of confirmations required before finalizing tree creation
	KnobNumRequiredConfirmations = "spark.so.num_required_confirmations"
	// KnobStaticDepositAddressPrivacyEnabled gates the per-wallet privacy filter on the
	// public query_static_deposit_addresses endpoint. Default off so the endpoint behaves
	// as before until the SSP has switched to the internal query_static_deposit_addresses
	// RPC; flip on per environment once that's deployed.
	KnobStaticDepositAddressPrivacyEnabled = "spark.so.privacy.static_deposit_addresses.enabled"
	KnobReadOnlyEndpoints                  = "spark.so.ro_session"
	KnobGossipLimit                        = "spark.so.gossip.limit"
	KnobResumeSendTransferLimit            = "spark.so.resume_send_transfer.limit"

	// KnobFinalizeRevealedTokenTransactionsBatchLimit caps how many revealed token
	// transactions the finalize cron processes per query iteration as it drains the backlog.
	KnobFinalizeRevealedTokenTransactionsBatchLimit = "spark.so.finalize_revealed_token_transactions.batch_limit"
	// KnobFinalizeRevealedTokenTransactionsMaxRuntimeSeconds bounds how long a single
	// finalize cron run keeps draining batches before yielding to the next scheduled run.
	KnobFinalizeRevealedTokenTransactionsMaxRuntimeSeconds = "spark.so.finalize_revealed_token_transactions.max_runtime_seconds"

	KnobEnablePartnerJWT = "spark.so.enable_partner_jwt"
	// KnobUseConsensusTransfer routes StartTransferV3 through the 2PC engine
	// instead of the legacy syncTransferV3Init + syncSettleSenderKeyTweaks fanout.
	//
	// The sweep does not need to undo coordinator domain writes: the engine
	// records the COMMITTED decision atomically with the coordinator's
	// SENDER_KEY_TWEAKED write (single request-tx DbCommit), so a crash before
	// that commit leaves the transfer untweaked with an IN_FLIGHT row (sweep →
	// ROLLED_BACK, consistent) and a crash after it leaves a COMMITTED row the
	// reconciler drives forward (SP-3195).
	KnobUseConsensusTransfer = "spark.so.use_consensus_transfer"
	// KnobUseConsensusClaim routes ClaimTransfer through the 2PC engine
	// instead of the legacy settleReceiverKeyTweakWithClaimPackage + finalize
	// gossip fanout. Interpreted as binary (any non-zero value enables) — not
	// a percentage rollout.
	//
	// The sweep does not need to undo coordinator domain writes: the engine
	// records the COMMITTED decision atomically with the coordinator's
	// RECEIVER_KEY_TWEAK_LOCKED / key-tweak writes (single request-tx DbCommit),
	// so a crash before that commit rolls those writes back with an IN_FLIGHT
	// row (sweep → ROLLED_BACK, consistent) and a crash after it leaves a
	// COMMITTED row the reconciler drives forward (SP-3195).
	KnobUseConsensusClaim = "spark.so.use_consensus_claim"

	// KnobUseConsensusCoopExit routes the TransferPackage (single-call) path of
	// CooperativeExitV2 through the 2PC engine instead of the legacy
	// syncCoopExitInit -> InitiateCooperativeExit fanout. Interpreted as binary
	// (any non-zero value enables) — not a percentage rollout.
	//
	// Unlike SEND_TRANSFER/CLAIM_TRANSFER, coop exit does NOT apply key tweaks in
	// Commit: the chain watcher (tweakKeysForCoopExit) applies them only after the
	// exit tx confirms on-chain. Prepare stores the key tweaks but leaves the
	// transfer at SENDER_INITIATED (a status the watcher leaves untouched); Commit
	// applies the refund signatures and promotes it to SENDER_KEY_TWEAK_PENDING.
	//
	// TODO: extend SweepStaleCoordinatorFlows to invoke FlowHandler.Rollback for
	// COOP_EXIT on the coordinator. Today the sweep only flips the FlowExecution
	// row to ROLLED_BACK — it does not undo the coordinator-side createTransfer +
	// CooperativeExit row, so a coordinator crash between engine DbCommit and
	// markCommitted leaves the coordinator stranded while participants reconcile
	// to RETURNED.
	KnobUseConsensusCoopExit = "spark.so.use_consensus_coop_exit"

	// KnobUseConsensusInitiatePreimageSwap routes InitiatePreimageSwapV3 through
	// the 2PC engine instead of the legacy initiatePreimageSwap fanout
	// (InitiatePreimageSwapV2 -> GetPreimageShare) + PreimageSwap gossip.
	// Interpreted as binary (any non-zero value enables) — not a percentage
	// rollout.
	//
	// Prepare validates + creates the transfer/preimage_request/user_signed_transaction
	// rows on every SO and produces FROST round-2 shares over the HTLC refund txs
	// (REASON_SEND); BuildCommitPayload aggregates those signatures and, on the
	// non-HODL REASON_RECEIVE path, recovers the preimage from a threshold of
	// shares. The engine records the COMMITTED decision atomically with the
	// coordinator's domain commit (single request-tx DbCommit), so a crash before
	// that commit rolls every SO back with an IN_FLIGHT row (sweep -> ROLLED_BACK,
	// consistent) and a crash after it leaves a COMMITTED row the reconciler drives
	// forward (SP-3195). Sender key tweaks for REASON_SEND are settled later by
	// ProvidePreimage, not in this flow — matching legacy.
	KnobUseConsensusInitiatePreimageSwap = "spark.so.use_consensus_initiate_preimage_swap"

	// KnobUseConsensusStaticDepositUtxoRefund routes InitiateStaticDepositUtxoRefund
	// through the 2PC engine instead of the legacy create_static_deposit_utxo_refund
	// fanout + RollbackUtxoSwap gossip + best-effort UtxoSwapCompleted. Interpreted
	// as binary (any non-zero value enables) — not a percentage rollout.
	//
	// The coordinator collects FROST round-1 commitments before Execute (keeping the
	// public RPC a single call); Prepare validates + creates the UtxoSwap CREATED row
	// on every SO and produces round-2 shares; BuildCommitPayload builds the
	// user-aggregated SigningResult, stores it, and completes the coordinator swap;
	// Commit completes participant swaps; Rollback cancels (REFUND swaps skip the
	// SP-3261 transfer-sent guard). The engine records the COMMITTED decision
	// atomically with the coordinator's domain commit (single request-tx DbCommit).
	//
	// Only first-time refunds go through 2PC. A refund against an already-COMPLETED
	// swap (the owner re-signing additional refund txs / fee-bumping) takes the
	// shared handleAlreadyRegisteredSwapOnRefund re-sign path regardless of this
	// knob — no FlowExecution row is written for those, so a "consensus-routed"
	// refund retry legitimately shows no consensus row when debugging.
	//
	// Rollout ordering: enable only after every SO runs a binary that dispatches
	// CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND (consensusFlowHandler) —
	// an SO without the handler fails prepare/commit/rollback for op type 9, and
	// the coordinator entrypoint has no legacy fallback, so a premature flip breaks
	// (not just degrades) all static-deposit refunds. Once the consensus-ownership fence is
	// deployed on EVERY SO, a stray legacy RollbackUtxoSwap can no longer wedge a consensus
	// swap — legacy rollback refuses consensus_managed rows — so no gossip drain is needed.
	// The fence only exists on upgraded binaries, so until it is fleet-wide (e.g.
	// mid-rolling-deploy) an un-upgraded SO can still cancel such a row; keep the
	// drain/canary gate until every SO carries the fence. Rows prepared by an un-upgraded SO
	// carry consensus_managed=false forever (the flag is immutable), so also let those
	// pre-fence consensus rows drain before relying on the fence alone.
	KnobUseConsensusStaticDepositUtxoRefund = "spark.so.use_consensus_static_deposit_utxo_refund"

	// KnobUseConsensusStaticDepositUtxoSwap routes the fixed-amount static deposit claim
	// through the 2PC consensus engine (0 = legacy, >0 = consensus). Enable only after every SO
	// dispatches CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_SWAP AND carries the
	// consensus-ownership fence (legacy rollback refusing consensus_managed rows). Once the
	// fence is fleet-wide a stray legacy RollbackUtxoSwap can't wedge a fresh consensus swap,
	// so no gossip drain is needed; until then (mid-rolling-deploy) keep the drain/canary gate
	// — an un-upgraded SO would still cancel the row, and rows it prepared stay
	// consensus_managed=false forever (the flag is immutable), so let those pre-fence rows
	// drain too.
	KnobUseConsensusStaticDepositUtxoSwap = "spark.so.use_consensus_static_deposit_utxo_swap"

	// KnobUseConsensusReserveInstantStaticDepositUtxoSwap routes phase one of the instant
	// static deposit claim through the 2PC consensus engine (0 = legacy, >0 = consensus).
	// Enable only after every SO dispatches the op type AND carries the consensus-ownership
	// fence. Once the fence is fleet-wide, a stray legacy rollback (in either knob-flip
	// direction) can't cancel an in-flight consensus reservation; until then
	// (mid-rolling-deploy) keep the drain/canary gate, since an un-upgraded SO would still
	// cancel the row — and rows it prepared stay consensus_managed=false forever (the flag is
	// immutable), so let those pre-fence rows drain too.
	KnobUseConsensusReserveInstantStaticDepositUtxoSwap = "spark.so.use_consensus_reserve_instant_static_deposit_utxo_swap"

	// KnobUseConsensusClaimInstantStaticDepositUtxoSwap routes phase two of the instant
	// static deposit claim through the 2PC consensus engine (0 = legacy, >0 = consensus),
	// folding the legacy save-utxo fanout, secondary transfer, separate spend-tx FROST round,
	// and best-effort completion fanout into one engine round. Enable only after every SO
	// dispatches CONSENSUS_OPERATION_TYPE_CLAIM_INSTANT_STATIC_DEPOSIT_UTXO_SWAP. The claim's
	// rollback never cancels the reservation, so the legacy-rollback drain hazard of the two
	// knobs above does not apply to this one.
	KnobUseConsensusClaimInstantStaticDepositUtxoSwap = "spark.so.use_consensus_claim_instant_static_deposit_utxo_swap"

	// KnobUseConsensusInitiateSwapPrimaryTransfer routes the swap v3 primary leg
	// (initiate_swap_primary_transfer) through the 2PC consensus engine (0 = legacy,
	// >0 = consensus; binary, not a percentage rollout), replacing the legacy
	// startTransferInternal fanout with an engine round whose Prepare signs the
	// adaptor-encumbered refunds on every SO and whose commit applies the aggregated
	// signatures without settling key tweaks (the counter leg settles both legs).
	// Enable only after every SO dispatches
	// CONSENSUS_OPERATION_TYPE_INITIATE_SWAP_PRIMARY_TRANSFER. Mixed-mode legs are
	// safe: both paths write identical Transfer rows, so a legacy-created primary
	// can be settled by a consensus counter and vice versa. One observable
	// difference: a consensus-created primary sits in SENDER_KEY_TWEAK_PENDING on
	// every SO (never SENDER_INITIATED_COORDINATOR), so the RPC response's transfer
	// status changes accordingly — verify SSP-side swap handling treats both as
	// "primary pending" before ramping beyond canary.
	KnobUseConsensusInitiateSwapPrimaryTransfer = "spark.so.use_consensus_initiate_swap_primary_transfer"

	// KnobUseConsensusInitiateCounterTransfer routes the swap v3 counter leg
	// (initiate_counter_transfer) through the 2PC consensus engine (0 = legacy,
	// >0 = consensus; binary, not a percentage rollout), replacing the legacy
	// fanout + SettleSwapKeyTweak gossip with an engine round whose commit applies
	// the aggregated adaptor signatures and settles BOTH legs' key tweaks in one
	// transaction on each SO. Enable only after every SO dispatches
	// CONSENSUS_OPERATION_TYPE_INITIATE_COUNTER_TRANSFER. No drain hazard with the
	// legacy path: the legacy retry gossip only fires for counter transfers in
	// coordinator-side statuses, which the consensus flow never uses, and
	// CommitSwapKeyTweaks is idempotent either way. For the same reason the
	// resume_send_transfer cron never sees consensus-routed swap transfers —
	// recovery for a wedged flow is the FlowExecution reconciler (stuck-participant
	// query + coordinator self-sweep), not the legacy cron.
	KnobUseConsensusInitiateCounterTransfer = "spark.so.use_consensus_initiate_counter_transfer"

	KnobShutdownHodlInvoices = "spark.so.shutdown_hodl_invoices"

	KnobMaxUnusedDepositAddresses = "spark.so.max_unused_deposit_addresses"

	// KnobMimoTransferMultiReceiverEnabled enables multi-input multi-output transfer support
	// with multiple independent receivers. When enabled, ClaimTransfer resolves the receiver
	// by identity public key and tracks per-receiver claim status.
	KnobMimoTransferMultiReceiverEnabled = "spark.so.mimo_transfer_multi_receiver_enabled"

	// When enabled, rotate_static_deposit_address creates a new address instead
	// of returning NotFound when no existing default address exists.

	// Enable instant static deposit flow.
	KnobEnableInstantStaticDeposit = "spark.so.enable_instant_static_deposit"
	// Total number of sats that can be pending in the instant static deposit flow
	KnobMaxPendingInstantStaticDepositAmount = "spark.so.max_pending_instant_static_deposit_amount"

	KnobPurgeDanglingSigningKeyshareSecretsBatchSize = "spark.so.purge_dangling_signing_keyshare_secrets_batch_size"
	// Per-run cap on the number of aged candidates a single purge invocation
	// will scan. Combined with the cursor persisted in memcache, this bounds
	// per-run DB load while still giving eventual full coverage of the table.
	KnobPurgeDanglingSigningKeyshareSecretsMaxScanCount = "spark.so.purge_dangling_signing_keyshare_secrets_max_scan_count"

	// Seconds a PARTICIPANT FlowExecution row can stay IN_FLIGHT before the
	// reconciliation task considers it stuck and asks the coordinator for
	// the final outcome. Must exceed the gossip retry interval so gossip
	// delivery gets a chance to finish on its own before we reconcile.
	KnobFlowExecutionStuckThreshold = "spark.so.flow_execution.stuck_threshold_seconds"

	// Seconds a COORDINATOR FlowExecution row can stay IN_FLIGHT before the
	// self-sweep task marks it ROLLED_BACK (presumed-abort). Set generously
	// above the longest legitimate 2PC decision window.
	KnobFlowExecutionCoordinatorStallThreshold = "spark.so.flow_execution.coordinator_stall_threshold_seconds"

	// Maximum number of stuck PARTICIPANT rows the reconciliation task
	// processes per tick. Caps blast radius if many rows get stuck at once.
	KnobFlowExecutionSweepBatchLimit = "spark.so.flow_execution.sweep_batch_limit"

	// Master switch for flow_execution.* metric emission. Disabled by default
	// (0). Set to a positive value to enable. When disabled, the reconcile
	// task still runs and recovers stuck rows; only the gauge sampling and
	// counter increments are skipped, so dashboards don't carry partial data
	// before the recovery feature is fully wired up. Flip this on once
	// alerting policies are in place.
	KnobFlowExecutionMetricsEnabled = "spark.so.flow_execution.metrics_enabled"

	// Minimum seconds an IN_FLIGHT row must have aged before it shows up in
	// the in_flight_count / oldest_in_flight_age_ms gauges. Younger rows are
	// still in the normal gossip-retry window and would just create noise on
	// dashboards. Default 600s (10 minutes).
	KnobFlowExecutionMetricsMinAgeSeconds = "spark.so.flow_execution.metrics_min_age_seconds"
)

type Config struct {
	Enabled   *bool   `yaml:"enabled"`
	Namespace *string `yaml:"namespace"`
	// StaticValues provides default knob values for local development
	// over Kubernetes-based knob providers.
	StaticValues map[string]float64 `yaml:"static_values"`
}

func (c *Config) IsEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// Knobs represents a collection of feature flags and their values
type Knobs interface {
	GetValue(knob string, defaultValue float64) float64
	GetValueTarget(knob string, target *string, defaultValue float64) float64
	GetDuration(knob string, defaultValue time.Duration) time.Duration
	GetDurationTarget(knob string, target *string, defaultValue time.Duration) time.Duration
	RolloutRandomTarget(knob string, target *string, defaultValue float64) bool
	RolloutRandom(knob string, defaultValue float64) bool
	RolloutUUIDTarget(knob string, id uuid.UUID, target *string, defaultValue float64) bool
	RolloutUUID(knob string, id uuid.UUID, defaultValue float64) bool
}

// Context helpers for passing Knobs service through request handling
type knobsContextKey struct{}

// InjectKnobsService returns a new context with the given Knobs service attached.
func InjectKnobsService(ctx context.Context, k Knobs) context.Context {
	return context.WithValue(ctx, knobsContextKey{}, k)
}

// GetKnobsService retrieves the Knobs service from context if present;
// otherwise creates a new empty Knobs service.
func GetKnobsService(ctx context.Context) Knobs {
	if ctx != nil {
		if v := ctx.Value(knobsContextKey{}); v != nil {
			if k, ok := v.(Knobs); ok {
				return k
			}
		}
	}
	return New(nil)
}

type knobsImpl struct {
	provider KnobsValuesProvider
}

type KnobsValuesProvider interface {
	GetValue(key string, defaultValue float64) float64
}

func New(knobsValuesProvider KnobsValuesProvider) *knobsImpl {
	return &knobsImpl{
		provider: knobsValuesProvider,
	}
}

func keyString(knob string, target *string) string {
	if target != nil {
		return fmt.Sprintf("%s@%s", knob, *target)
	}
	return knob
}

// GetValueTarget retrieves a knob value for a specific target
func (k knobsImpl) GetValueTarget(knob string, target *string, defaultValue float64) float64 {
	if k.provider == nil {
		return defaultValue
	}
	return k.provider.GetValue(keyString(knob, target), defaultValue)
}

// GetValue retrieves a knob value without a target
func (k knobsImpl) GetValue(knob string, defaultValue float64) float64 {
	return k.GetValueTarget(knob, nil, defaultValue)
}

// GetDurationTarget returns a duration interpreted from a knob value with target in seconds.
// If the knob is nil or resolves to a non-positive value, the defaultDuration is returned.
func (k knobsImpl) GetDurationTarget(knob string, target *string, defaultDuration time.Duration) time.Duration {
	seconds := k.GetValueTarget(knob, target, defaultDuration.Seconds())
	if seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return defaultDuration
}

// GetDuration returns a duration interpreted from a knob value in seconds.
// If the knob is nil or resolves to a non-positive value, the defaultDuration is returned.
func (k knobsImpl) GetDuration(knob string, defaultDuration time.Duration) time.Duration {
	return k.GetDurationTarget(knob, nil, defaultDuration)
}

// RolloutRandomTarget determines if a feature should be rolled out based on a random value.
// This function uses pseudo-random number generation to decide feature rollouts.
//
// Parameters:
//   - knob: The name of the feature flag/knob to check
//   - defaultValue: Default rollout percentage (0-100) to use if no specific value is configured
//   - target: Optional target identifier for environment-specific rollouts (can be nil)
//
// Returns:
//   - true if the feature should be rolled out for this request
//   - false if the feature should not be rolled out
//
// The function first checks for a target-specific value (if target is provided),
// then falls back to the defaultValue. The value is expected to be in the range 0-100
// where 0 means never roll out (0%) and 100 means always roll out (100%).
//
// Note: This function uses rand.Float64() which means results are not deterministic
// across different calls, unlike RolloutUUIDTarget which is deterministic.
func (k knobsImpl) RolloutRandomTarget(knob string, target *string, defaultValue float64) bool {
	value := defaultValue
	if v := k.GetValueTarget(knob, target, defaultValue); v != defaultValue {
		value = v
	}
	return rand.Float64() < value/100.0
}

// RolloutRandom determines if a feature should be rolled out based on a random value without a target
func (k knobsImpl) RolloutRandom(knob string, defaultValue float64) bool {
	return k.RolloutRandomTarget(knob, nil, defaultValue)
}

// RolloutUUIDTarget determines if a feature should be rolled out based on a UUID.
// This function uses deterministic hash-based calculation to decide feature rollouts.
//
// Parameters:
//   - knob: The name of the feature flag/knob to check
//   - id: UUID to use for deterministic rollout calculation
//   - defaultValue: Default rollout percentage (0-100) to use if no specific value is configured
//   - target: Optional target identifier for environment-specific rollouts (can be nil)
//
// Returns:
//   - true if the feature should be rolled out for this UUID
//   - false if the feature should not be rolled out
//
// The function first checks for a target-specific value (if target is provided),
// then falls back to the defaultValue. The value is expected to be in the range 0-100
// where 0 means never roll out (0%) and 100 means always roll out (100%).
//
// Algorithm:
// 1. Creates an MD5 hash of the knob name as a salt
// 2. XORs the UUID with the salt to create a deterministic value
// 3. Takes modulo 100000 of the result
// 4. Compares against threshold (value * 1000) to determine rollout
//
// Key characteristics:
//   - Deterministic: Same knob+UUID combination always returns the same result
//   - Uniform distribution: UUIDs are distributed evenly across rollout percentages
//   - Stable: Results remain consistent across application restarts
//   - Independent: Different knobs with same UUID can have different results
func (k knobsImpl) RolloutUUIDTarget(knob string, id uuid.UUID, target *string, defaultValue float64) bool {
	value := defaultValue
	if v := k.GetValueTarget(knob, target, defaultValue); v != defaultValue {
		value = v
	}

	// Calculate salt using MD5 (128 bits)
	hash := md5.Sum([]byte(knob))
	salt := new(big.Int).SetBytes(hash[:])

	// UUID as big.Int (128 bits)
	uuidInt := new(big.Int).SetBytes(id[:])

	// XOR the UUID with the salt
	salted := new(big.Int).Xor(uuidInt, salt)

	// salted % 100000 < value * 1000
	mod := new(big.Int).Mod(salted, big.NewInt(100000))
	threshold := int64(value * 1000)
	return mod.Int64() < threshold
}

// RolloutUUID determines if a feature should be rolled out based on a UUID without a target
func (k knobsImpl) RolloutUUID(knob string, id uuid.UUID, defaultValue float64) bool {
	return k.RolloutUUIDTarget(knob, id, nil, defaultValue)
}

type fixedKnobs struct {
	values map[string]float64
}

func NewEmptyFixedKnobs() Knobs {
	return &fixedKnobs{values: map[string]float64{}}
}

// NewFixedKnobs creates a new Knobs instance that simply maps fixed strings to
// values. It ignores the provider. This is useful for testing and development
// purposes and almost certainly should not be used in production.
func NewFixedKnobs(values map[string]float64) Knobs {
	return &fixedKnobs{values: values}
}

func (m fixedKnobs) GetValueTarget(knob string, target *string, defaultValue float64) float64 {
	key := knob
	if target != nil {
		key = fmt.Sprintf("%s@%s", knob, *target)
	}

	if value, exists := m.values[key]; exists {
		return value
	}
	return defaultValue
}

func (m fixedKnobs) GetValue(knob string, defaultValue float64) float64 {
	return m.GetValueTarget(knob, nil, defaultValue)
}

func (m fixedKnobs) RolloutRandomTarget(knob string, target *string, defaultValue float64) bool {
	value := m.GetValueTarget(knob, target, defaultValue)
	return value > 0
}

func (m fixedKnobs) RolloutRandom(knob string, defaultValue float64) bool {
	return m.RolloutRandomTarget(knob, nil, defaultValue)
}

func (m fixedKnobs) RolloutUUIDTarget(knob string, id uuid.UUID, target *string, defaultValue float64) bool {
	value := m.GetValueTarget(knob, target, defaultValue)
	return value > 0
}

func (m fixedKnobs) RolloutUUID(knob string, id uuid.UUID, defaultValue float64) bool {
	return m.RolloutUUIDTarget(knob, id, nil, defaultValue)
}

// GetDurationTarget returns a duration interpreted from a knob value with target in seconds.
// If the knob is nil or resolves to a non-positive value, the defaultDuration is returned.
func (m fixedKnobs) GetDurationTarget(knob string, target *string, defaultDuration time.Duration) time.Duration {
	seconds := m.GetValueTarget(knob, target, defaultDuration.Seconds())
	if seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return defaultDuration
}

// GetDuration returns a duration interpreted from a knob value in seconds.
// If the knob is nil or resolves to a non-positive value, the defaultDuration is returned.
func (m fixedKnobs) GetDuration(knob string, defaultDuration time.Duration) time.Duration {
	return m.GetDurationTarget(knob, nil, defaultDuration)
}
