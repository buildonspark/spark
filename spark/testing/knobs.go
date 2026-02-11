package sparktesting

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	knobsNamespace  = "knobs"
	knobsConfigMap  = "knobs"
	propagationWait = 500 * time.Millisecond
)

// KnobController provides functionality to manipulate knob values in the K8s ConfigMap
// for integration testing. It captures the original ConfigMap state on creation and
// automatically restores it when the test finishes via t.Cleanup().
type KnobController struct {
	client   kubernetes.Interface
	original map[string]string
	mu       sync.Mutex
}

// NewKnobController creates a new KnobController that can get/set/delete knob values
// in the K8s ConfigMap. The original ConfigMap state is captured and will be
// automatically restored when the test finishes.
func NewKnobController(t *testing.T) (*KnobController, error) {
	client := getKubernetesClient(t)

	controller := &KnobController{
		client:   client,
		original: make(map[string]string),
	}

	// Capture original ConfigMap state for restoration
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	if configMap.Data != nil {
		maps.Copy(controller.original, configMap.Data)
	}

	// Register cleanup to restore original state
	t.Cleanup(func() {
		//nolint: usetesting // Use background context to ensure cleanup runs even if test is cancelled.
		// t.Context is canceled just before Cleanup-registered functions are called.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		if err := controller.restoreOriginal(cleanupCtx); err != nil {
			t.Errorf("Failed to restore knobs ConfigMap during cleanup: %v", err)
		}
	})

	return controller, nil
}

// SetKnob sets a knob value in the K8s ConfigMap. The value is stored as a string
// representation of the float64 (e.g., "100" for 100.0).
// After setting, it waits briefly for the change to propagate to the SOs.
func (k *KnobController) SetKnob(t *testing.T, knobName string, value float64) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	configMap, err := k.client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	if configMap.Data == nil {
		configMap.Data = make(map[string]string)
	}

	configMap.Data[knobName] = strconv.FormatFloat(value, 'f', -1, 64)

	_, err = k.client.CoreV1().ConfigMaps(knobsNamespace).Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update knobs ConfigMap: %w", err)
	}

	// Wait for propagation to SOs via K8s watch mechanism
	time.Sleep(propagationWait)

	return nil
}

// GetKnob retrieves a knob value from the K8s ConfigMap.
// Returns the value, whether the knob exists, and any error.
func (k *KnobController) GetKnob(t *testing.T, knobName string) (float64, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	configMap, err := k.client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return 0, false, fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	if configMap.Data == nil {
		return 0, false, nil
	}

	valueStr, exists := configMap.Data[knobName]
	if !exists {
		return 0, false, nil
	}

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return 0, true, fmt.Errorf("failed to parse knob value %q: %w", valueStr, err)
	}

	return value, true, nil
}

// DeleteKnob removes a knob from the K8s ConfigMap.
// After deletion, it waits briefly for the change to propagate to the SOs.
func (k *KnobController) DeleteKnob(t *testing.T, knobName string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	configMap, err := k.client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	if configMap.Data == nil {
		return nil
	}

	delete(configMap.Data, knobName)

	_, err = k.client.CoreV1().ConfigMaps(knobsNamespace).Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update knobs ConfigMap: %w", err)
	}

	// Wait for propagation to SOs via K8s watch mechanism
	time.Sleep(propagationWait)

	return nil
}

// RestoreOriginal restores the ConfigMap to its original state captured at construction time.
// This is called automatically via t.Cleanup(), but can also be called manually.
func (k *KnobController) RestoreOriginal(t *testing.T) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	return k.restoreOriginal(ctx)
}

// restoreOriginal is the internal implementation that takes a context directly.
// This allows cleanup to use context.Background() instead of t.Context().
func (k *KnobController) restoreOriginal(ctx context.Context) error {
	configMap, err := k.client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	// Replace entire data with original
	configMap.Data = make(map[string]string)
	maps.Copy(configMap.Data, k.original)

	_, err = k.client.CoreV1().ConfigMaps(knobsNamespace).Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to restore knobs ConfigMap: %w", err)
	}

	// Wait for propagation to SOs via K8s watch mechanism
	time.Sleep(propagationWait)

	return nil
}

// RunWithKnob runs a test function with the knob set to a specific value,
// then restores the original value afterward. This is useful for subtests
// that need different knob values.
func (k *KnobController) RunWithKnob(t *testing.T, knobName string, value float64, fn func()) {
	original, existed, err := k.GetKnob(t, knobName)
	if err != nil {
		t.Fatalf("Failed to get original knob value: %v", err)
	}

	if err := k.SetKnob(t, knobName, value); err != nil {
		t.Fatalf("Failed to set knob value: %v", err)
	}

	defer func() {
		if existed {
			if err := k.SetKnob(t, knobName, original); err != nil {
				t.Errorf("Failed to restore knob value: %v", err)
			}
		} else {
			if err := k.DeleteKnob(t, knobName); err != nil {
				t.Errorf("Failed to delete knob: %v", err)
			}
		}
	}()

	fn()
}

// SetKnobWithTarget sets a knob value with environment-specific targeting.
// The value is stored in YAML map format (e.g., "REGTEST: 100").
// After setting, it waits briefly for the change to propagate to the SOs.
//
// Example: To set a knob to 100 for REGTEST environment:
//
//	controller.SetKnobWithTarget(t, "my.feature.enabled", "REGTEST", 100)
func (k *KnobController) SetKnobWithTarget(t *testing.T, knobName string, target string, value float64) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	configMap, err := k.client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	if configMap.Data == nil {
		configMap.Data = make(map[string]string)
	}

	// Format as YAML map for target-specific values.
	// Quote the target to prevent YAML from interpreting numeric strings as integers.
	configMap.Data[knobName] = fmt.Sprintf("\"%s\": %s", target, strconv.FormatFloat(value, 'f', -1, 64))

	_, err = k.client.CoreV1().ConfigMaps(knobsNamespace).Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update knobs ConfigMap: %w", err)
	}

	// Wait for propagation to SOs via K8s watch mechanism
	time.Sleep(propagationWait)

	return nil
}

// SetKnobForTestMain sets a knob value for use in TestMain functions where *testing.T is not available.
// Unlike SetKnob, this does not register cleanup - the caller is responsible for restoration.
// Returns the previous value (if any) and whether it existed, for manual restoration.
func SetKnobForTestMain(knobName string, value float64) (previousValue float64, existed bool, err error) {
	config, err := getKubernetesConfig()
	if err != nil {
		return 0, false, fmt.Errorf("failed to get kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return 0, false, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return 0, false, fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	// Capture previous value for restoration
	if configMap.Data != nil {
		if valueStr, ok := configMap.Data[knobName]; ok {
			previousValue, err = strconv.ParseFloat(valueStr, 64)
			if err != nil {
				return 0, false, fmt.Errorf("failed to parse previous knob value: %w", err)
			}
			existed = true
		}
	}

	if configMap.Data == nil {
		configMap.Data = make(map[string]string)
	}

	configMap.Data[knobName] = strconv.FormatFloat(value, 'f', -1, 64)

	_, err = client.CoreV1().ConfigMaps(knobsNamespace).Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return 0, false, fmt.Errorf("failed to update knobs ConfigMap: %w", err)
	}

	time.Sleep(propagationWait)

	return previousValue, existed, nil
}

// DeleteKnobForTestMain removes a knob for use in TestMain functions.
func DeleteKnobForTestMain(knobName string) error {
	config, err := getKubernetesConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	configMap, err := client.CoreV1().ConfigMaps(knobsNamespace).Get(ctx, knobsConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get knobs ConfigMap: %w", err)
	}

	if configMap.Data == nil {
		return nil
	}

	delete(configMap.Data, knobName)

	_, err = client.CoreV1().ConfigMaps(knobsNamespace).Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update knobs ConfigMap: %w", err)
	}

	time.Sleep(propagationWait)

	return nil
}

// getKubernetesConfig returns the kubernetes config, preferring kubeconfig file.
func getKubernetesConfig() (*rest.Config, error) {
	kubeconfigPath := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}
