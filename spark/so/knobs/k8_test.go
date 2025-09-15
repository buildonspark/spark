package knobs

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKnobsK8ValuesProvider_HandleConfigMap(t *testing.T) {
	provider := &knobsK8ValuesProvider{
		context: t.Context(),
		logger:  zap.NewNop(),
		lock:    &sync.RWMutex{},
		values:  make(map[string]float64),
	}

	tests := []struct {
		name           string
		configMapData  map[string]string
		expectedValues map[string]float64
	}{
		{
			name: "simple scalar values",
			configMapData: map[string]string{
				"key1":  "1",
				"key2":  "2.5",
				"scale": "100",
			},
			expectedValues: map[string]float64{
				"key1":  1.0,
				"key2":  2.5,
				"scale": 100.0,
			},
		},
		{
			name: "target-specific values using YAML syntax",
			configMapData: map[string]string{
				"spark.ssp.use_fixed_trees.enabled": "REGTEST: 100.0\nMAINNET: 50.0\n",
				"feature.rollout":                   "target1: 25.0\ntarget2: 75.0\n",
			},
			expectedValues: map[string]float64{
				"spark.ssp.use_fixed_trees.enabled@REGTEST": 100.0,
				"spark.ssp.use_fixed_trees.enabled@MAINNET": 50.0,
				"feature.rollout@target1":                   25.0,
				"feature.rollout@target2":                   75.0,
			},
		},
		{
			name: "mixed simple and target-specific values",
			configMapData: map[string]string{
				"simple_knob":    "42.0",
				"complex_knob":   "env1: 10.0\nenv2: 20.0\n",
				"another_simple": "3.14",
			},
			expectedValues: map[string]float64{
				"simple_knob":       42.0,
				"complex_knob@env1": 10.0,
				"complex_knob@env2": 20.0,
				"another_simple":    3.14,
			},
		},
		{
			name: "empty and invalid values",
			configMapData: map[string]string{
				"valid_knob":   "123.45",
				"invalid_yaml": "invalid: yaml: content",
			},
			expectedValues: map[string]float64{
				"valid_knob": 123.45,
				// invalid_yaml should not be added
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear existing values
			provider.lock.Lock()
			provider.values = make(map[string]float64)
			provider.lock.Unlock()

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "knobs",
					Namespace: "knobs",
				},
				Data: tt.configMapData,
			}

			provider.handleConfigMap(configMap)

			// Verify expected values
			provider.lock.RLock()
			actualValues := make(map[string]float64)
			for key, value := range provider.values {
				actualValues[key] = value
			}
			provider.lock.RUnlock()

			assert.Equal(t, tt.expectedValues, actualValues, "ConfigMap values should match expected values")
		})
	}
}

func TestKnobsK8ValuesProvider_HandleConfigMap_NilData(t *testing.T) {
	provider := &knobsK8ValuesProvider{
		context: t.Context(),
		logger:  zap.NewNop(),
		lock:    &sync.RWMutex{},
		values:  make(map[string]float64),
	}

	// Add an existing value
	provider.lock.Lock()
	provider.values["existing_knob"] = 42.0
	provider.lock.Unlock()

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "knobs",
			Namespace: "knobs",
		},
		Data: nil,
	}

	provider.handleConfigMap(configMap)

	provider.lock.RLock()
	_, exists := provider.values["existing_knob"]
	provider.lock.RUnlock()

	assert.False(t, exists, "Existing knob should be removed")
}

func TestKnobsK8ValuesProvider_GetValue(t *testing.T) {
	provider := &knobsK8ValuesProvider{
		context: t.Context(),
		logger:  zap.NewNop(),
		lock:    &sync.RWMutex{},
		values:  make(map[string]float64),
	}

	// Set some test values
	provider.lock.Lock()
	provider.values["test_knob"] = 50.0
	provider.values["test_knob@target1"] = 75.0
	provider.lock.Unlock()

	// Test GetValue functionality
	assert.InDelta(t, 50.0, provider.GetValue("test_knob", 0.0), 0.001)
	assert.InDelta(t, 0.0, provider.GetValue("non_existent_knob", 0.0), 0.001)
	assert.InDelta(t, 42.0, provider.GetValue("non_existent_knob", 42.0), 0.001)
}
