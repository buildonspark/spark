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
	KnobDatabaseOnlyCommitDirty           = "spark.database.only_commit_dirty"
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

	KnobSoSigningCommitmentNodeLimit  = "spark.so.signing_commitments.nodes_limit"
	KnobSoSigningCommitmentCountLimit = "spark.so.signing_commitments.count_limit"

	KnobGrpcServerMethodEnabled       = "spark.so.grpc.server.method.enabled"
	KnobGrpcServerConnectionTimeout   = "spark.so.grpc.server.connection_timeout"
	KnobGrpcServerKeepaliveTime       = "spark.so.grpc.server.keepalive_time"
	KnobGrpcServerKeepaliveTimeout    = "spark.so.grpc.server.keepalive_timeout"
	KnobGrpcServerUnaryHandlerTimeout = "spark.so.grpc.server.unary_handler_timeout"

	KnobGrpcServerConcurrencyLimitLimit     = "spark.so.grpc.server.concurrency_limit.limit"
	KnobGrpcServerConcurrencyExcludeIps     = "spark.so.grpc.server.concurrency_limit.exclude_ips"
	KnobGrpcServerConcurrencyExcludePubkeys = "spark.so.grpc.server.concurrency_limit.exclude_pubkeys"

	KnobSoMaxTransactionsPerRequest         = "spark.so.max_transactions_per_request"
	KnobSoMaxKeysharesPerRequest            = "spark.so.max_keyshares_per_request"
	KnobGRPCClientTimeout                   = "spark.so.grpc.client.timeout"
	KnobGrpcClientPoolMinConnections        = "spark.so.grpc.client.pool.min_connections"
	KnobGrpcClientPoolMaxConnections        = "spark.so.grpc.client.pool.max_connections"
	KnobGrpcClientPoolIdleTimeoutSeconds    = "spark.so.grpc.client.pool.idle_timeout_seconds"
	KnobGrpcClientPoolMaxLifetimeSeconds    = "spark.so.grpc.client.pool.max_lifetime_seconds"
	KnobGrpcClientPoolUsersPerConnectionCap = "spark.so.grpc.client.pool.users_per_connection_cap"
	KnobGrpcClientPoolScaleConcurrency      = "spark.so.grpc.client.pool.scale_concurrency"

	KnobSoDkgBatchSize = "spark.so.dkg.batch_size"

	KnobRequireDirectFromCPFPRefund   = "spark.so.require_direct_from_cpfp_refund"
	KnobBaseDirectTimelockOnNonDirect = "spark.so.base_direct_timelock_on_non_direct"

	// Task / gocron related knobs.
	KnobSoTaskEnabled = "spark.so.task.enabled"
	KnobSoTaskTimeout = "spark.so.task.timeout"

	// Watch Chain
	// Set to 0 to disable updating exiting Tree Nodes in Chain Watcher.
	// DANGEROUS: Disabling it can lead to loss of funds.
	KnobWatchChainMarkExitingNodesEnabled = "spark.so.watch_chain.mark_exiting_nodes.enabled"

	// Tokens
	KnobUseNumericAmountForCurrentTokenSupply   = "spark.so.tokens.use_numeric_amount_for_current_token_supply"
	KnobReclaimRemappedOutputsIfRevealRequested = "spark.so.tokens.reclaim_remapped_outputs_if_reveal_requested"
	KnobFinalizeCreatedSignedOutputsJustInTime  = "spark.so.tokens.finalize_created_signed_outputs_just_in_time"
	KnobTokenTransactionV3Enabled               = "spark.so.tokens.token_transaction_v3_enabled"
	KnobAllowExtraMetadataOnMainnet             = "spark.so.tokens.allow_extra_metadata_on_mainnet"

	// Number of confirmations required before finalizing tree creation
	KnobNumRequiredConfirmations = "spark.so.num_required_confirmations"
	KnobPrivacyEnabled           = "spark.so.privacy.enabled"
	KnobReadOnlyEndpoints        = "spark.so.ro_session"
	KnobGossipLimit              = "spark.so.gossip.limit"
	KnobResumeSendTransferLimit  = "spark.so.resume_send_transfer.limit"

	// Enable more rigorous checks for finalize signature requests. See SPARK-236
	KnobEnableStrictFinalizeSignature        = "spark.so.enable_strict_finalize_signature"
	KnobEnableStrictDirectRefundTxValidation = "spark.so.enable_strict_direct_refund_tx_validation"

	KnobEnableDepositFlowValidation       = "spark.so.enable_deposit_flow_validation"
	KnobSoEnhancedBitcoinTxValidation     = "spark.so.enhanced_bitcoin_tx_validation"
	KnobEnhancedTransferReceiveValidation = "spark.so.enhanced_transfer_receive_validation"
)

type Config struct {
	Enabled   *bool   `yaml:"enabled"`
	Namespace *string `yaml:"namespace"`
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
