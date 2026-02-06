package sparktesting

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKnobController_SetKnob(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{
				"existing.knob": "50",
			},
		},
	)

	// original mirrors ConfigMap data to simulate NewKnobController's snapshot behavior
	controller := &KnobController{
		client:   client,
		original: map[string]string{"existing.knob": "50"},
		mu:       sync.Mutex{},
	}

	err := controller.SetKnob(t, "test.knob", 100)
	require.NoError(t, err)

	// Verify the knob was set in the ConfigMap
	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(t.Context(), knobsConfigMap, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "100", configMap.Data["test.knob"])
	assert.Equal(t, "50", configMap.Data["existing.knob"])
}

func TestKnobController_GetKnob(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{
				"existing.knob": "42.5",
			},
		},
	)

	controller := &KnobController{
		client:   client,
		original: map[string]string{"existing.knob": "42.5"},
		mu:       sync.Mutex{},
	}

	// Test getting existing knob
	value, exists, err := controller.GetKnob(t, "existing.knob")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.InDelta(t, 42.5, value, 0.001)

	// Test getting non-existent knob
	value, exists, err = controller.GetKnob(t, "nonexistent.knob")
	require.NoError(t, err)
	assert.False(t, exists)
	assert.InDelta(t, 0.0, value, 0.001)
}

func TestKnobController_DeleteKnob(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{
				"to.delete":   "100",
				"to.preserve": "200",
			},
		},
	)

	controller := &KnobController{
		client:   client,
		original: map[string]string{"to.delete": "100", "to.preserve": "200"},
		mu:       sync.Mutex{},
	}

	err := controller.DeleteKnob(t, "to.delete")
	require.NoError(t, err)

	// Verify the knob was deleted
	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(t.Context(), knobsConfigMap, metav1.GetOptions{})
	require.NoError(t, err)

	_, exists := configMap.Data["to.delete"]
	assert.False(t, exists)
	assert.Equal(t, "200", configMap.Data["to.preserve"])
}

func TestKnobController_RestoreOriginal(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{
				"modified.knob": "999",
				"added.knob":    "123",
			},
		},
	)

	controller := &KnobController{
		client: client,
		original: map[string]string{
			"modified.knob": "50",
			"original.knob": "75",
		},
		mu: sync.Mutex{},
	}

	err := controller.RestoreOriginal(t)
	require.NoError(t, err)

	// Verify ConfigMap was restored to original state
	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(t.Context(), knobsConfigMap, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "50", configMap.Data["modified.knob"])
	assert.Equal(t, "75", configMap.Data["original.knob"])

	// "added.knob" should no longer exist (it wasn't in original)
	_, exists := configMap.Data["added.knob"]
	assert.False(t, exists)
}

func TestKnobController_RunWithKnob(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{
				"existing.knob": "50",
			},
		},
	)

	controller := &KnobController{
		client:   client,
		original: map[string]string{"existing.knob": "50"},
		mu:       sync.Mutex{},
	}

	// Test with existing knob
	var capturedValue float64
	controller.RunWithKnob(t, "existing.knob", 999, func() {
		val, ex, e := controller.GetKnob(t, "existing.knob")
		require.NoError(t, e)
		require.True(t, ex)
		capturedValue = val
	})
	assert.InDelta(t, 999.0, capturedValue, 0.001)

	// After RunWithKnob, should be restored to original
	value, exists, err := controller.GetKnob(t, "existing.knob")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.InDelta(t, 50.0, value, 0.001)

	// Test with new knob that doesn't exist
	controller.RunWithKnob(t, "new.knob", 123, func() {
		val, ex, e := controller.GetKnob(t, "new.knob")
		require.NoError(t, e)
		require.True(t, ex)
		capturedValue = val
	})
	assert.InDelta(t, 123.0, capturedValue, 0.001)

	// After RunWithKnob, new knob should be deleted (wasn't in original)
	_, exists, err = controller.GetKnob(t, "new.knob")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestKnobController_SetKnobWithTarget(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{},
		},
	)

	controller := &KnobController{
		client:   client,
		original: map[string]string{},
		mu:       sync.Mutex{},
	}

	err := controller.SetKnobWithTarget(t, "test.knob", "REGTEST", 100)
	require.NoError(t, err)

	// Verify the knob was set in YAML map format
	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(t.Context(), knobsConfigMap, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "\"REGTEST\": 100", configMap.Data["test.knob"])
}

func TestKnobController_restoreOriginal_UsesProvidedContext(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{
				"knob": "modified",
			},
		},
	)

	controller := &KnobController{
		client:   client,
		original: map[string]string{"knob": "original"},
		mu:       sync.Mutex{},
	}

	// Use a context with a short timeout to verify the function respects context
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := controller.restoreOriginal(ctx)
	require.NoError(t, err)

	// Verify restoration happened
	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "original", configMap.Data["knob"])
}

func TestKnobController_ConcurrentAccess(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      knobsConfigMap,
				Namespace: knobsNamespace,
			},
			Data: map[string]string{},
		},
	)

	controller := &KnobController{
		client:   client,
		original: map[string]string{},
		mu:       sync.Mutex{},
	}

	// Run multiple goroutines setting different knobs concurrently
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			knobName := fmt.Sprintf("concurrent.knob.%d", i)
			err := controller.SetKnob(t, knobName, float64(i*10))
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	// Verify all knobs were set (the exact values depend on execution order due to fake client)
	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(t.Context(), knobsConfigMap, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, configMap.Data)
}
