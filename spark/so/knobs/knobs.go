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
	KnobDatabaseStatementTimeout         = "spark.database.statement_timeout"
	KnobDatabaseLockTimeout              = "spark.database.lock_timeout"
	KnobRateLimitPeriod                  = "spark.so.ratelimit.period"
	KnobRateLimitLimit                   = "spark.so.ratelimit.limit"
	KnobRateLimitMethods                 = "spark.so.ratelimit.methods"
	KnobSoRollbackUtxoSwapUsingGossip    = "spark.so.rollback_utxo_swap_using_gossip"
	KnobSoTransferLimit                  = "spark.so.transfer_limit"
	KnobGrpcServerMethodEnabled          = "spark.so.grpc.server.method.enabled"
	KnobGrpcServerConnectionTimeout      = "spark.so.grpc.server.connection_timeout"
	KnobGrpcServerKeepaliveTime          = "spark.so.grpc.server.keepalive_time"
	KnobGrpcServerKeepaliveTimeout       = "spark.so.grpc.server.keepalive_timeout"
	KnobGrpcServerUnaryHandlerTimeout    = "spark.so.grpc.server.unary_handler_timeout"
	KnobGrpcServerConcurrencyLimitLimit  = "spark.so.grpc.server.concurrency_limit.limit"
	KnobSoGenerateStaticDepositAddressV2 = "spark.so.generate_static_deposit_address_v2"
	KnobSoMaxTransactionsPerRequest      = "spark.so.max_transactions_per_request"
	KnobSoMaxKeysharesPerRequest         = "spark.so.max_keyshares_per_request"
	KnobGRPCClientTimeout                = "spark.so.grpc.client.timeout"
	KnobRenewLeafDisabled                = "spark.so.grpc.server.disable_renew_leaf"
)

type Config struct {
	Enabled *bool `yaml:"enabled"`
}

func (c *Config) IsEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// Knobs represents a collection of feature flags and their values
type Knobs interface {
	GetValue(knob string, defaultValue float64) float64
	GetValueTarget(knob string, target *string, defaultValue float64) float64
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

// GetDurationSeconds returns a duration interpreted from a knob value in seconds.
// If the knob is nil or resolves to a non-positive value, the defaultDuration is returned.
func GetDurationSeconds(k Knobs, knob string, defaultDuration time.Duration) time.Duration {
	if k == nil {
		return defaultDuration
	}
	seconds := k.GetValue(knob, defaultDuration.Seconds())
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return defaultDuration
}

// GetTargetDurationSeconds returns a duration interpreted from a knob value with target in seconds.
// If the knob is nil or resolves to a non-positive value, the defaultDuration is returned.
func GetTargetDurationSeconds(k Knobs, knob string, target *string, defaultDuration time.Duration) time.Duration {
	if k == nil {
		return defaultDuration
	}
	seconds := k.GetValueTarget(knob, target, defaultDuration.Seconds())
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return defaultDuration
}
