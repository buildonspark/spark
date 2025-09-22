package knobs

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestKnobs() Knobs {
	// Use NewFixedKnobs for testing instead of trying to create knobsImpl directly
	return NewFixedKnobs(make(map[string]float64))
}

func TestKnobs(t *testing.T) {
	k := newTestKnobs()

	// Test GetValue with no value set
	value := k.GetValue("test_knob", 0.0)
	assert.Zero(t, value)

	// Test GetDuration with default value
	assert.Equal(t, 5*time.Second, k.GetDuration("test_knob", 5*time.Second))

	// Test RolloutRandom with default value
	assert.True(t, k.RolloutRandom("test_knob", 100.0)) // 100% chance
	assert.False(t, k.RolloutRandom("test_knob", 0.0))  // 0% chance

	// Test RolloutUUID with default value
	id := uuid.New()
	assert.True(t, k.RolloutUUID("test_knob", id, 100.0)) // 100% chance
	assert.False(t, k.RolloutUUID("test_knob", id, 0.0))  // 0% chance

	// Test target-specific values
	// Create a new knobs instance with specific values for testing
	testValues := map[string]float64{
		"test_knob@target1":          50.0,
		"test_knob@target2":          0.0,
		"test_duration_knob@target1": 3.0,
		"test_duration_knob@target2": 1.5,
	}
	k = NewFixedKnobs(testValues)

	target1 := "target1"
	target2 := "target2"

	// Test GetValueTarget
	value = k.GetValueTarget("test_knob", &target1, 0.0)
	assert.InDelta(t, 50.0, value, 0.001)

	value = k.GetValueTarget("test_knob", &target2, 0.0)
	assert.InDelta(t, 0.0, value, 0.001)

	assert.Equal(t, 3*time.Second, k.GetDurationTarget("test_duration_knob", &target1, 5*time.Second))
	assert.Equal(t, 1500*time.Millisecond, k.GetDurationTarget("test_duration_knob", &target2, 5*time.Second))

	// Test RolloutRandomTarget
	assert.False(t, k.RolloutRandomTarget("test_knob", &target2, 100.0)) // 0% chance

	// Test RolloutUUIDTarget
	assert.False(t, k.RolloutUUIDTarget("test_knob", id, &target2, 1.0)) // 0% chance

	// Test RolloutUUIDTarget with 100% chance (default value)
	assert.True(t, k.RolloutUUIDTarget("non_existent_knob", id, nil, 100.0))      // 100% chance, no target
	assert.True(t, k.RolloutUUIDTarget("non_existent_knob", id, &target1, 100.0)) // 100% chance, target doesn't exist

	// Add a target with 100% chance
	testValues["test_knob@target_100"] = 100.0
	k = NewFixedKnobs(testValues)

	target100 := "target_100"
	assert.True(t, k.RolloutUUIDTarget("test_knob", id, &target100, 0.0)) // 100% chance from target value

	// Test with different UUIDs to ensure deterministic behavior
	id2 := uuid.New()
	id3 := uuid.New()

	// These should be consistent for the same knob+UUID combination
	result1 := k.RolloutUUIDTarget("test_knob", id, &target1, 50.0)
	result2 := k.RolloutUUIDTarget("test_knob", id, &target1, 50.0)
	assert.Equal(t, result1, result2, "RolloutUUIDTarget should be deterministic for same inputs")

	// Different UUIDs with same knob should potentially give different results
	result3 := k.RolloutUUIDTarget("test_knob", id2, &target1, 50.0)
	result4 := k.RolloutUUIDTarget("test_knob", id3, &target1, 50.0)
	// Note: We can't assert they're different since it depends on the hash, but we test they're consistent
	result3Repeat := k.RolloutUUIDTarget("test_knob", id2, &target1, 50.0)
	result4Repeat := k.RolloutUUIDTarget("test_knob", id3, &target1, 50.0)
	assert.Equal(t, result3, result3Repeat, "RolloutUUIDTarget should be deterministic for id2")
	assert.Equal(t, result4, result4Repeat, "RolloutUUIDTarget should be deterministic for id3")
}

func TestKnobs_RolloutUUIDConsistent(t *testing.T) {
	// Create a real knobsImpl instance to test the actual UUID algorithm
	k := New(nil)

	// Test specific UUIDs for deterministic rollout behavior matching Python implementation
	// Values verified with Python using knob="test" and default=50.0
	testCases := []struct {
		uuidStr  string
		expected bool
	}{
		{"25291dc3-35ad-4a88-b7d6-c010afa821f5", false}, // mod=91395, threshold=50000
		{"c0516611-6db1-4ad7-ab70-e69441308b6b", true},  // mod=3453, threshold=50000
	}

	for _, tc := range testCases {
		t.Run(tc.uuidStr, func(t *testing.T) {
			parsedUUID, err := uuid.Parse(tc.uuidStr)
			require.NoError(t, err, "Should be able to parse UUID")

			result := k.RolloutUUID("test", parsedUUID, 50.0)
			assert.Equal(t, tc.expected, result,
				"RolloutUUID should return consistent result for UUID %s with default 50%%", tc.uuidStr)

			// Test multiple times to ensure consistency - this is the key requirement
			for i := 0; i < 10; i++ {
				repeatResult := k.RolloutUUID("test", parsedUUID, 50.0)
				assert.Equal(t, tc.expected, repeatResult, "RolloutUUID should be deterministic (iteration %d)", i)
			}
		})
	}
}

// Mock provider for testing New function
type mockKnobsProvider struct {
	valuesToSet map[string]float64
	err         error
}

func (m *mockKnobsProvider) GetValue(key string, defaultValue float64) float64 {
	if m.err != nil {
		return defaultValue
	}

	if value, exists := m.valuesToSet[key]; exists {
		return value
	}
	return defaultValue
}

func TestKnobs_New(t *testing.T) {
	t.Run("New with nil provider", func(t *testing.T) {
		knobs := New(nil)
		require.NotNil(t, knobs, "New should return non-nil knobs")

		// Should return default values when no provider
		value := knobs.GetValue("test_knob", 42.0)
		assert.InDelta(t, 42.0, value, 0.001, "Should return default value when no provider")
	})

	t.Run("New with valid provider", func(t *testing.T) {
		provider := &mockKnobsProvider{
			valuesToSet: map[string]float64{
				"init_knob":         100.0,
				"init_knob@target1": 50.0,
			},
		}

		knobs := New(provider)
		require.NotNil(t, knobs, "New should return non-nil knobs")

		// Values should be retrieved from provider
		assert.InDelta(t, 100.0, knobs.GetValue("init_knob", 0.0), 0.001)
		target1 := "target1"
		assert.InDelta(t, 50.0, knobs.GetValueTarget("init_knob", &target1, 0.0), 0.001)
	})

	t.Run("Provider error handling", func(t *testing.T) {
		provider := &mockKnobsProvider{
			err: fmt.Errorf("simulated error"),
		}

		knobs := New(provider)
		require.NotNil(t, knobs, "New should return non-nil knobs even with erroring provider")

		// Should return default values when provider errors
		value := knobs.GetValue("test_knob", 42.0)
		assert.InDelta(t, 42.0, value, 0.001, "Should return default value when provider errors")
	})
}

func TestRolloutEdgeCases(t *testing.T) {
	k := newTestKnobs()

	t.Run("RolloutUUID boundary values", func(t *testing.T) {
		id := uuid.New()

		// Test extreme percentages
		assert.False(t, k.RolloutUUID("test", id, -10.0), "Negative percentage should be false")
		assert.False(t, k.RolloutUUID("test", id, 0.0), "0% should be false")
		assert.True(t, k.RolloutUUID("test", id, 100.0), "100% should be true")
		assert.True(t, k.RolloutUUID("test", id, 150.0), "Over 100% should be true")

		// Test very small percentages
		result1 := k.RolloutUUID("test", id, 0.001)
		result2 := k.RolloutUUID("test", id, 0.001)
		assert.Equal(t, result1, result2, "Very small percentages should still be deterministic")
	})

	t.Run("RolloutUUIDTarget with extreme values", func(t *testing.T) {
		// Create a new knobs instance with specific values for testing
		testValues := map[string]float64{
			"extreme@target": -50.0,
			"over@target":    500.0,
		}
		k = NewFixedKnobs(testValues)

		target := "target"
		id := uuid.New()

		assert.False(t, k.RolloutUUIDTarget("extreme", id, &target, 50.0), "Negative stored value should be false")
		assert.True(t, k.RolloutUUIDTarget("over", id, &target, 0.0), "Very high stored value should be true")
	})

	t.Run("Empty string knob names", func(t *testing.T) {
		id := uuid.New()

		result1 := k.RolloutUUID("", id, 50.0)
		result2 := k.RolloutUUID("", id, 50.0)
		assert.Equal(t, result1, result2, "Empty knob names should still be deterministic")

		value := k.GetValue("", 42.0)
		assert.InDelta(t, 42.0, value, 0.001, "Empty knob name should return default")
	})

	t.Run("Special characters in knob names", func(t *testing.T) {
		specialKnobs := []string{
			"knob.with.dots",
			"knob-with-dashes",
			"knob_with_underscores",
			"knob@with@ats",
			"knob with spaces",
			"knob/with/slashes",
		}

		id := uuid.New()
		for _, knobName := range specialKnobs {
			result1 := k.RolloutUUID(knobName, id, 50.0)
			result2 := k.RolloutUUID(knobName, id, 50.0)
			require.Equal(t, result1, result2, "Knob '%s' should be deterministic", knobName)
		}
	})
}
